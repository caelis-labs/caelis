"""Offline accounting for the frozen study. Never sends a request or reruns a Turn."""
import collections
import hashlib
import json
from pathlib import Path
import sys

root = Path(sys.argv[1]).resolve()
manifest_bytes = (root / 'manifest.json').read_bytes()
manifest = json.loads(manifest_bytes)


def load(path):
    return json.loads(path.read_bytes()) if path.exists() else None


def encode(value):
    # A defined value-byte metric; these are not tokenizer estimates.
    return json.dumps(value, ensure_ascii=False, separators=(',', ':')).encode()


def digest(raw):
    return hashlib.sha256(raw).hexdigest()


def totals(generations):
    answer = {'generations': len(generations), 'complete_responses': 0,
              'missing_usage_generations': [], 'missing_cached_generations': []}
    counters = collections.Counter()
    for generation in generations:
        response = generation.get('response')
        if response and response.get('turn_complete'):
            answer['complete_responses'] += 1
        usage = generation.get('accounted_usage') or (response or {}).get('usage')
        reported = usage and (usage.get('reported') or any(usage.get(k, 0) for k in ['prompt_tokens', 'completion_tokens', 'total_tokens']))
        if not reported:
            answer['missing_usage_generations'].append(generation['id'])
            continue
        for k in ['prompt_tokens', 'completion_tokens', 'reasoning_tokens', 'total_tokens']:
            counters[k] += usage.get(k, 0)
        cached = usage.get('cached_input_tokens_reported') or 'cached_input_tokens' in usage
        if not cached:
            answer['missing_cached_generations'].append(generation['id'])
        else:
            counters['cached_input_tokens'] += usage.get('cached_input_tokens', 0)
    answer['reported_usage_subtotal'] = dict(counters)
    # Do not turn missing/incomplete counters into synthesized complete usage.
    valid = bool(generations) and not answer['missing_usage_generations'] and answer['complete_responses'] == len(generations)
    answer['complete_usage'] = dict(counters) if valid else None
    if answer['complete_usage'] is not None:
        if answer['missing_cached_generations']:
            answer['complete_usage']['cached_input_tokens'] = None
            answer['complete_usage']['fresh_input_tokens'] = None
        else:
            answer['complete_usage']['fresh_input_tokens'] = counters['prompt_tokens'] - counters['cached_input_tokens']
    return answer


sessions = []
all_generations = []
for plan in manifest['schedule']:
    directory = root / plan['id']
    result = {**plan, 'started': directory.exists(), 'turns': [], 'generations': []}
    if not directory.exists():
        sessions.append(result)
        continue
    result['session_ref'] = load(directory / 'session-ref.json')
    generations = []
    for path in sorted(directory.glob('generation-*.meta.json')):
        stem = path.name.removesuffix('.meta.json')
        meta = load(path)
        response = load(directory / (stem + '.response.json'))
        receipt = load(directory / (stem + '.receipt.json'))
        request = load(directory / (stem + '.request.json'))
        result_bytes = (directory / (stem + '.prefix.json')).read_bytes()
        generation = {**meta, 'id': stem, 'response': response, 'receipt': receipt,
                      'prefix_observed_sha256': digest(result_bytes), 'prefix_observed_bytes': len(result_bytes)}
        assert meta['prefix_sha256'] == generation['prefix_observed_sha256']
        calls = []
        if response:
            scripts = directory / 'scripts'
            for i, part in enumerate(response.get('message', {}).get('parts', [])):
                call = part.get('tool_use')
                if call:
                    calls.append(call['name'])
                    if call['name'] == 'js_exec' and 'code' in call.get('input', {}):
                        scripts.mkdir(exist_ok=True)
                        (scripts / f'{stem}-{i}.js').write_text(call['input']['code'])
        generation['model_tool_calls'] = calls
        generation['http'] = []
        for wire in sorted(directory.glob('wire-*.meta.json')):
            info = load(wire)
            if info.get('generation') == meta['generation']:
                generation['http'].append({**info, 'stem': wire.name.removesuffix('.meta.json')})
        accounted = dict((response or {}).get('usage') or {})
        raw_usage_records = []
        for http in generation['http']:
            stream = directory / (http['stem'] + '.response.raw')
            if not stream.exists():
                continue
            terminal_usage = None
            for line in stream.read_text().splitlines():
                if not line.startswith('data: '):
                    continue
                try:
                    event = json.loads(line[6:])
                except ValueError:
                    continue
                candidate = event.get('response', {}).get('usage')
                if candidate and event.get('type') in ['response.completed', 'response.done']:
                    terminal_usage = candidate
            if terminal_usage:
                raw_usage_records.append({'wire': http['stem'], 'usage': terminal_usage})
        generation['raw_terminal_usage'] = raw_usage_records
        if len(raw_usage_records) == 1:
            raw_usage = raw_usage_records[0]['usage']
            # Normalized Go Usage omits an explicit cached_tokens=0. Recover
            # only that directly observed zero, never invent an absent count.
            details = raw_usage.get('input_tokens_details') or {}
            if 'cached_tokens' in details:
                accounted['cached_input_tokens'] = details['cached_tokens']
                accounted['cached_input_tokens_reported'] = True
            generation['usage_crosscheck'] = {
                'prompt_equal': accounted.get('prompt_tokens', 0) == raw_usage.get('input_tokens'),
                'completion_equal': accounted.get('completion_tokens', 0) == raw_usage.get('output_tokens'),
                'total_equal': accounted.get('total_tokens', 0) == raw_usage.get('total_tokens')}
        generation['accounted_usage'] = accounted
        generations.append(generation)
        all_generations.append({**generation, 'session': plan['id'], 'pair': plan['pair'], 'route': plan['route']})
    result['generations'] = [{k: v for k, v in g.items() if k != 'response'} for g in generations]
    main = [g for g in generations if g['kind'] == 'main']
    result['sdk_main_prefix_hash_counts'] = dict(collections.Counter(g['prefix_observed_sha256'] for g in main))
    result['sdk_main_prefix_byte_counts'] = dict(collections.Counter(g['prefix_observed_bytes'] for g in main))
    main_http = [h for g in main for h in g['http']]
    result['http_main_instructions_tools_hash_counts'] = dict(collections.Counter(h['instructions_tools_value_sha256'] for h in main_http))
    result['http_main_instructions_tools_byte_counts'] = dict(collections.Counter(h['instructions_tools_value_bytes'] for h in main_http))
    result['prefix_claim'] = 'serialized instructions/tools values only; not provider KV evidence or whole-body common-prefix proof'
    for turn in [1, 2]:
        group = [g for g in generations if g['turn'] == turn]
        assertions = load(directory / f'turn-{turn}.assertions.json')
        receipts = load(directory / f'turn-{turn}.receipts.json') or {}
        events = load(directory / f'turn-{turn}.events.json') or []
        canonical_outputs = [e['message'] for e in events if e.get('type') == 'tool_result' and e.get('visibility') == 'canonical' and e.get('message')]
        counters = collections.Counter((r['tool_name'], r['status']) for r in receipts.values())
        turn_result = {'turn': turn, 'label': manifest['turns'][turn-1]['label'], 'assertions': assertions,
                       'all_requests': totals(group),
                       'main': totals([g for g in group if g['kind'] == 'main']),
                       'selector': totals([g for g in group if g['kind'] == 'selector']),
                       'main_model_function_call_counts': dict(collections.Counter(name for g in group if g['kind']=='main' for name in g['model_tool_calls'])),
                       'selector_function_call_counts': dict(collections.Counter(name for g in group if g['kind']=='selector' for name in g['model_tool_calls'])),
                       'model_function_call_counts_including_selector': dict(collections.Counter(name for g in group for name in g['model_tool_calls'])),
                       'original_journal_latest_counts': {name + ' / ' + status: count for (name, status), count in counters.items()},
                       'canonical_tool_result_message_bytes': sum(len(encode(m)) for m in canonical_outputs),
                       'canonical_tool_result_messages': len(canonical_outputs),
                       'answer_file_bytes': (directory / f'turn-{turn}.answer.json').stat().st_size if (directory / f'turn-{turn}.answer.json').exists() else None,
                       'query_evidence': []}
        definitions = load(directory / 'directory.json') or {}
        for receipt in receipts.values():
            definition = definitions.get(receipt['tool_name'], {})
            remote = definition.get('metadata', {}).get('caelis.mcp.tool')
            if remote not in ['query-docs', 'get-library-docs', 'resolve-library-id']:
                continue
            tool_result = receipt.get('result') or {}
            if isinstance(tool_result, str):
                tool_result = json.loads(tool_result)
            content = tool_result.get('content')
            turn_result['query_evidence'].append({'identity': receipt['identity'], 'tool': receipt['tool_name'], 'remote': remote,
                'status': receipt['status'], 'input': receipt.get('input'), 'content_sha256': digest(encode(content)),
                'content_bytes': len(encode(content)), 'original_result': content})
        result['turns'].append(turn_result)
    result['all_requests'] = totals(generations)
    result['worker_metrics'] = [json.loads(line) for line in (directory / 'worker-metrics.jsonl').read_text().splitlines()] if (directory / 'worker-metrics.jsonl').exists() else []
    sessions.append(result)

comparison = []
for pair in [1, 2]:
    for turn in [1, 2]:
        row = {'pair': pair, 'turn': turn}
        for route in ['native', 'js']:
            row[route] = totals([g for g in all_generations if g['pair'] == pair and g['turn'] == turn and g['route'] == route])
        comparison.append(row)
aggregate = []
for label, turn in [('first', 1), ('continuation', 2), ('combined', None)]:
    row = {'label': label}
    for route in ['native', 'js']:
        row[route] = totals([g for g in all_generations if g['route'] == route and (turn is None or g['turn'] == turn)])
    aggregate.append(row)

summary = {'manifest_sha256': digest(manifest_bytes), 'production_base': manifest['production_base'],
           'accounting': 'Provider-normalized final Response usage counted once per generation, including selectors. Original invocation receipts kept separately; partial invocation usage is not fabricated as a completed Response. fresh=prompt-cached only when both are reported. Explicit cached_tokens=0 is read from the original terminal SSE usage when the normalized Go struct omits it; raw/normalized prompt and completion are crosschecked. Output bytes are UTF-8 compact normalized canonical tool-result Message values, once per canonical event; no repeated-history multiplication and no byte-to-token conversion.',
           'cache_boundary': 'Four independent local Sessions do not imply provider-side cache isolation. Counterbalanced descriptive sample, not a statistical guarantee.',
           'stop': load(root / 'STOPPED.json'), 'complete': load(root / 'COMPLETED.json'),
           'sessions': sessions, 'paired_turns': comparison, 'aggregate': aggregate}
(root / 'analysis.json').write_text(json.dumps(summary, ensure_ascii=False, indent=2) + '\n')
print(json.dumps({'completed': bool(summary['complete']), 'stopped': bool(summary['stop']), 'sessions': [{'id': s['id'], 'started': s['started'], 'requests': len(s.get('generations', []))} for s in sessions], 'aggregate': aggregate}, ensure_ascii=False, indent=2))
