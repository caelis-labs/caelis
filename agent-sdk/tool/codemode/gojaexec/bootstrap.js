// Trusted glue is closed over pristine intrinsics before untrusted code runs.
// The only native function accepts bounded strings and returns a Promise.
(function(rpc, limits) {
  "use strict";
  const parse = JSON.parse, keys = Object.keys, isArray = Array.isArray;
  const finite = Number.isFinite, freeze = Object.freeze, define = Object.defineProperty;
  const ErrorType = Error, SetType = Set;
  const apply = Function.prototype.call.bind(Function.prototype.call);
  const charCode = Function.prototype.call.bind(String.prototype.charCodeAt);
  const slice = Function.prototype.call.bind(String.prototype.slice);
  const then = Function.prototype.call.bind(Promise.prototype.then);
  const has = Function.prototype.call.bind(Set.prototype.has);
  const add = Function.prototype.call.bind(Set.prototype.add);
  const del = Function.prototype.call.bind(Set.prototype.delete);
  // Streaming JSON encoding bounds bytes before appending. It deliberately
  // accepts only JSON values (no toJSON, BigInt, functions or undefined), and
  // caps depth. It never exports an untrusted object graph to Go.
  function encode(value, budget) {
    let out = "", used = 0;
    const seen = new SetType();
    function put(s, n) {
      if ((used += n) > budget) throw new ErrorType("JSON byte budget exceeded");
      out += s;
    }
    function quote(s) {
      put('"', 1);
      for (let i = 0; i < s.length; i++) {
        const c = charCode(s, i);
        if (c === 34 || c === 92) put("\\" + s[i], 2);
        else if (c < 32 || (c >= 0xd800 && c <= 0xdfff &&
          !(c <= 0xdbff && i + 1 < s.length && charCode(s,i+1) >= 0xdc00 && charCode(s,i+1) <= 0xdfff))) {
          const hex = "0123456789abcdef";
          put("\\u"+hex[(c>>12)&15]+hex[(c>>8)&15]+hex[(c>>4)&15]+hex[c&15],6);
        } else if (c >= 0xd800 && c <= 0xdbff) {put(slice(s,i,i+2),4);i++;}
        else put(s[i], c < 128 ? 1 : c < 2048 ? 2 : 3);
      }
      put('"',1);
    }
    function visit(v, depth) {
      if (depth > 128) throw new ErrorType("JSON depth exceeded");
      if (v === null) return put("null",4);
      switch (typeof v) {
        case "string": return quote(v);
        case "boolean": return put(v ? "true" : "false", v ? 4 : 5);
        case "number": {if (!finite(v)) throw new ErrorType("non-finite JSON number");const s=""+v;return put(s,s.length);}
        case "object": break;
        default: throw new ErrorType("return and arguments must contain only JSON values");
      }
      if (has(seen,v)) throw new ErrorType("cyclic JSON");
      add(seen,v);
      const array=isArray(v);put(array?"[":"{",1);
      if (array) {
        for (let i=0;i<v.length;i++) {if(i)put(",",1);visit(v[i],depth+1);}
      } else {
        // keys() can allocate proportional to the input object. Goja has no
        // heap limiter; encoding/IPC budgets do not claim to bound the VM heap.
        const names=keys(v);
        for(let i=0;i<names.length;i++){if(i)put(",",1);quote(names[i]);put(":",1);visit(v[names[i]],depth+1);}
      }
      put(array?"]":"}",1);del(seen,v);
    }
    visit(value,0);return out;
  }
  function decode(raw) {
    const envelope=parse(raw), reply=parse(envelope.raw);
    define(reply,"raw_json",{value:envelope.raw,enumerable:false});
    if (envelope.error) {const e=new ErrorType(envelope.error);e.reply=reply;throw e;}
    return reply;
  }
  const tools=freeze({
    call(name,args) {
      if(typeof name!=="string" || name.length>1024 || args===null || typeof args!=="object" || isArray(args))
        throw new ErrorType("tools.call(name,args) requires a name string and JSON object");
      return then(rpc("call",name,encode(args,limits.argument_bytes)),decode);
    },
    search(query) {
      if(typeof query!=="string" || query.length===0 || query.length>1024)throw new ErrorType("tools.search(query) requires a non-empty string; search only unknown extension names");
      return then(then(rpc("search",query,"{}"),decode),r=>r.parts[0].json);
    },
    describe(name) {
      if(typeof name!=="string" || name.length===0 || name.length>1024)throw new ErrorType("tools.describe(name) requires a name string, e.g. searchResult.tools[0].name; reuse valid Session schemas with tools.call");
      return then(then(rpc("describe",name,"{}"),decode),r=>r.parts[0].json);
    }
  });
  const texts=[];let textBytes=0;
  const output=freeze({
    text(value) {
      const encoded=encode(value,limits.output_bytes-textBytes);textBytes+=encoded.length;
      if(texts.length>=limits.calls)throw new ErrorType("output count exceeded");
      texts.push(parse(encoded));
    },
    image(handle) {
      if(typeof handle!=="string" || handle.length>1024)throw new ErrorType("invalid media handle");
      return then(rpc("emit",handle,""),decode);
    }
  });
  return freeze({tools, output, encode: value => encode(texts.length ? {output:texts,result:value===undefined?null:value} : value===undefined?null:value,limits.output_bytes)});
})
