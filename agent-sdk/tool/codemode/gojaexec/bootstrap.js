// Trusted glue retains pristine intrinsics and private identities across scripts.
(function(rpc) {
  "use strict";
  const globals=globalThis, SyntaxErrorType=SyntaxError;
  const parse=JSON.parse, stringify=JSON.stringify, keys=Object.keys, isArray=Array.isArray;
  const finite=Number.isFinite, integer=Number.isInteger, freeze=Object.freeze, define=Object.defineProperty;
  const descriptor=Object.getOwnPropertyDescriptor, assign=Object.assign;
  const create=Object.create, ErrorType=Error, SetType=Set, WeakMapType=WeakMap;
  const weakGet=Function.prototype.call.bind(WeakMap.prototype.get);
  const weakSet=Function.prototype.call.bind(WeakMap.prototype.set);
  const own=Function.prototype.call.bind(Object.prototype.hasOwnProperty);
  const charCode=Function.prototype.call.bind(String.prototype.charCodeAt);
  const slice=Function.prototype.call.bind(String.prototype.slice);
  const then=Function.prototype.call.bind(Promise.prototype.then);
  const test=Function.prototype.call.bind(RegExp.prototype.test);
  const identifier=/^[A-Za-z_$][A-Za-z0-9_$]*$/;
  const has=Function.prototype.call.bind(Set.prototype.has);
  const add=Function.prototype.call.bind(Set.prototype.add);
  const del=Function.prototype.call.bind(Set.prototype.delete);
  const forEach=Function.prototype.call.bind(Set.prototype.forEach);
  const origins=new WeakMapType(), replies=new WeakMapType(), toolGlobals=new SetType();
  let limits, state, preferredPaths;
  function ownReply(reply, diagnostic = false) {
    const media=[], mediaHandles=new SetType();
    let mediaCount=0;
    for(let i=0;i<(reply.parts||[]).length;i++) {
      const p=reply.parts[i];
      if(p.kind==="media"||p.kind==="file_ref") {
        weakSet(origins,p,{kind:p.kind,handle:p.handle});freeze(p);
        if(!has(mediaHandles,p.handle)){add(mediaHandles,p.handle);mediaCount++;}
        define(media,media.length,{value:p,enumerable:true});
      }
    }
    const body={media:freeze(media)};
    const text=reply.body_text;
    if(diagnostic) {
      // Failed replies keep the legacy readers for diagnostic scripts.
      let read=false, json;
      define(body,"text",{value:()=>text});
      define(body,"json",{value:()=>{if(!read){json=parse(text);read=true;}return json;}});
    } else {
      define(body,"text",{value:text,enumerable:true});
      const hasData = own(reply,"value");
      define(body,"data",{value:hasData ? reply.value : undefined,enumerable:hasData});
    }
    const selector=reply.origin||reply.call_id;
    weakSet(replies,body,{selector,mediaCount,parts:selector?undefined:reply.parts,diagnosticText:diagnostic?text:undefined});
    return freeze(body);
  }
  // Output encoding bounds bytes before appending. Argument encoding has no
  // byte budget. Both accept only JSON values (no toJSON, BigInt, functions or
  // undefined) and cap depth. Neither exports an untrusted object graph to Go.
  function encode(value, budget, project) {
    let out = "", used = 0;
    const seen = new SetType(), selected=new SetType(), media=[];
    const original=project&&value&&typeof value==="object"?weakGet(replies,value):null;
    function put(s, n) {
      used += n;
      if (budget !== undefined && used > budget) throw new ErrorType("JSON byte budget exceeded");
      out += s;
    }
    function quote(s) {
      if (budget === undefined) {
        out += stringify(s);
        return;
      }
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
    function visit(v, depth, inspect, path) {
      path=path||"$";
      function invalid(reason) {return new ErrorType("invalid_input at "+slice(path,0,256)+": "+reason);}
      if (depth > 128) throw invalid("JSON depth exceeded");
      if (v === null) return put("null",4);
      switch (typeof v) {
        case "string": return quote(v);
        case "boolean": return put(v ? "true" : "false", v ? 4 : 5);
        case "number": {if (!finite(v)) throw invalid("non-finite JSON number");const s=""+v;return put(s,s.length);}
        case "object": break;
        default: throw invalid("only JSON values are supported; omit undefined fields or use null");
      }
      if (has(seen,v)) throw invalid("cyclic JSON");
      add(seen,v);
      const body=inspect&&weakGet(replies,v);
      if(body && typeof v.text === "function") {
        del(seen,v);
        return visit({text:body.diagnosticText,media:v.media},depth,inspect,path);
      }
      const origin=weakGet(origins,v);
      if(inspect&&origin&&(origin.kind==="media"||origin.kind==="file_ref")&&!has(selected,origin.handle)) {
        if(media.length>=limits.calls)throw new ErrorType("returned media budget exceeded");
        add(selected,origin.handle);
        define(media,media.length,{value:{kind:origin.kind,handle:origin.handle},enumerable:true});
      }
      const array=isArray(v);put(array?"[":"{",1);
      if (array) {
        const length=v.length;
        for (let i=0;i<length;i++) {if(i)put(",",1);visit(v[i],depth+1,inspect,path+"["+i+"]");}
      } else {
        // keys() can allocate proportional to the input object. Goja has no
        // heap limiter; encoding/IPC budgets do not claim to bound the VM heap.
        const names=keys(v);
        for(let i=0;i<names.length;i++) {
          const name=names[i];
          if(i)put(",",1);quote(name);put(":",1);
          // All projection decisions consume these same budgeted reads. Proxy
          // descriptors and accessors never supply a separate preflight value.
          const child=v[name];
          visit(child,depth+1,inspect,slice(path+"."+name,0,256));
        }
      }
      put(array?"]":"}",1);del(seen,v);
    }
    if(original) {
      if(original.mediaCount>limits.calls)throw new ErrorType("returned media budget exceeded");
      if(original.selector){put('{"reply":true,"call_id":',24);quote(original.selector);put('}',1);}
      else {put('{"reply":true,"parts":',21);visit(original.parts||[],0,false);put('}',1);}
      return out;
    }

    visit(value,0,!!project);
    if(project) {
      const body=out, bytes=used;out="";used=0;
      put('{"value":',9);put(body,bytes);put(',"parts":',9);visit(media,0,false);put('}',1);
    }
    return out;
  }
  function decode(raw, search = false) {
    const envelope=parse(raw), reply=parse(envelope.raw);
    if (envelope.error || reply.is_error || reply.outcome==="failed" || reply.outcome==="unknown_outcome") {
      let message=envelope.error || "Tool call failed";
      if(reply.outcome==="unknown_outcome") message="Unknown outcome; do not replay";
      else if(!envelope.error) {
        const parts=reply.parts||[];
        for(let i=0;i<parts.length;i++) {
          const p=parts[i], detail=p.kind==="text"?p.text:p.kind==="json"&&p.json?p.json.error:null;
          if(typeof detail==="string") {message="Tool call failed: "+slice(detail,0,1024);break;}
        }
      }
      const e=new ErrorType(message);e.reply=ownReply(reply,true);e.result=e.reply;
      e.code=envelope.code||(reply.outcome==="unknown_outcome"?"unknown_outcome":"tool_failed");
      throw e;
    }
    if(search) {
      if(!own(reply,"value")) throw new ErrorType("search result is missing its value");
      const value=reply.value;
      function prefer(entry) {
        const route=preferredPaths[entry.name];
        const current=route && descriptor(globals,route.root);
        if(current && own(current,"value") && current.value===route.target) entry.call_path=route.expression;
      }
      if(own(value,"tools")) {
        for(let i=0;i<value.tools.length;i++) {
          const item=value.tools[i];
          if(own(item,"entry")) prefer(item.entry);
          else if(!own(item,"error_code")) prefer(item);
        }
      } else prefer(value);
      return reply.value;
    }
    return ownReply(reply);
  }
  const api=assign(create(null),{
    call(name,args) {
      if(typeof name!=="string" || name.length>1024 || args===null || typeof args!=="object" || isArray(args))
        throw new ErrorType("tools.call(name,args) requires a name string and JSON object");
      return then(rpc("call",name,encode(args)),decode);
    },
    search(query={}) {
      if(!query||typeof query!=="object"||isArray(query))throw new ErrorType("tools.search requires an object");
      const names=keys(query), request={};
      for(let i=0;i<names.length;i++){
        const key=names[i], value=query[key];
        if(key==="names") {
          const count=isArray(value)?value.length:0, list=[];
          if(!integer(count)||count<1||count>16)throw new ErrorType("expected 1 to 16 tool names");
          for(let j=0;j<count;j++) {
            const name=value[j];
            if(typeof name!=="string"||!name.length||name.length>1024)throw new ErrorType("expected non-empty tool name");
            define(list,j,{value:name,enumerable:true});
          }
          request.names=list;
          continue;
        }
        if(key!=="name"&&key!=="query"&&key!=="after")throw new ErrorType("unsupported tools.search field: "+key);
        if(typeof value!=="string"||!value.length||value.length>1024)throw new ErrorType("expected non-empty search field");
        request[key]=value;
      }
      if((own(request,"name")||own(request,"names"))&&names.length!==1)throw new ErrorType("use name or names for exact lookup");
      return then(rpc("search","",encode(request)),raw=>decode(raw,true));
    }
  });
  // All entry points delegate to the same validation, RPC, decode and identity
  // path. Null prototypes preserve exact names, including __proto__.
  function property(name) {
    return test(identifier,name) ? "."+name : "["+stringify(name)+"]";
  }
  function install(names) {
  const tools=assign(create(null),api);
  const native=create(null), mcp=create(null), plugins=create(null), shortcuts=create(null);
  function table(parent,name) {
    if(!own(parent,name)) define(parent,name,{value:create(null),enumerable:true});
    return parent[name];
  }
  function route(parent,key,name,path) {
    let invoke;
    if(own(parent,key)) invoke=()=>{throw new ErrorType("ambiguous tool path: "+path);};
    else invoke=args=>tools.call(name,args);
    // Promise assimilation passes resolve/reject, never a JSON args object.
    // A tool literally called then remains available as then(args).
    if(key==="then") {
      const call=invoke;
      invoke=function(args,reject) {
        if(typeof args==="function"&&typeof reject==="function")
          return reject(new ErrorType("tool namespaces cannot be awaited"));
        return call(args);
      };
    }
    define(parent,key,{value:invoke,enumerable:true,configurable:true});
    return invoke;
  }
  function shortcut(name,value) {
    if(own(shortcuts,name)) shortcuts[name]=null;
    else define(shortcuts,name,{value,writable:true,enumerable:true});
  }
  const entries=names||[], methods=create(null);
  for(let i=0;i<entries.length;i++) {
    const entry=entries[i];
    if(!entry.server) {
      route(native,entry.name,entry.name,"tools.native["+entry.name+"]");
      shortcut(entry.name,native[entry.name]);
    } else {
      const invoke=route(table(mcp,entry.server),entry.tool,entry.name,"tools.mcp["+entry.server+"]["+entry.tool+"]");
      methods[entry.name]=invoke;
      if(entry.prefix) shortcut(entry.name,invoke);
      if(entry.plugin) route(table(table(plugins,entry.plugin),entry.server),entry.tool,entry.name,
        "tools.plugins["+entry.plugin+"]["+entry.server+"]["+entry.tool+"]");
    }
  }
  const servers=keys(mcp);
  for(let i=0;i<servers.length;i++) {
    const server=servers[i];freeze(mcp[server]);shortcut(server,mcp[server]);
  }
  const pluginNames=keys(plugins);
  for(let i=0;i<pluginNames.length;i++) {
    const group=plugins[pluginNames[i]], groupServers=keys(group);
    for(let j=0;j<groupServers.length;j++)freeze(group[groupServers[j]]);
    freeze(group);
  }
  tools.native=freeze(native);tools.mcp=freeze(mcp);tools.plugins=freeze(plugins);
  freeze(tools);
  forEach(toolGlobals,name=>{
    const prior=descriptor(globals,name);
    if(prior && prior.configurable) delete globals[name];
    del(toolGlobals,name);
  });
  const shortcutNames=keys(shortcuts);
  for(let i=0;i<shortcutNames.length;i++) {
    const name=shortcutNames[i];
    if(name!=="tools"&&name!=="runtime"&&!own(globals,name)&&shortcuts[name]!==null) {
      define(globals,name,{value:shortcuts[name],enumerable:true,configurable:true});add(toolGlobals,name);
    }
  }
  preferredPaths=create(null);
  for(let i=0;i<entries.length;i++) {
    const entry=entries[i], name=entry.name;
    // Qualify installed shortcuts so a caller's local binding cannot shadow them.
    if((!entry.server||entry.prefix) && has(toolGlobals,name) && shortcuts[name]!==null) {
      preferredPaths[name]={root:name,target:shortcuts[name],expression:"globalThis"+property(name)+"(args)"};
    } else if(entry.server && has(toolGlobals,entry.server) && shortcuts[entry.server]===mcp[entry.server] &&
      mcp[entry.server][entry.tool]===methods[name]) {
      preferredPaths[name]={root:entry.server,target:mcp[entry.server],expression:"globalThis"+property(entry.server)+property(entry.tool)+"(args)"};
    }
  }
  define(globals,"tools",{value:tools,configurable:true});
  return tools;
  }
  const bindings=create(null);
  function publish(name,cell) {
    if(!own(bindings,name)) {
      del(toolGlobals,name);
      define(globals,name,{enumerable:true,get(){
        return bindings[name].read();
      },set(value){
        const cell=bindings[name];
        if(cell.write) cell.write(value);
        // Assigning to a published const in a later script replaces only the
        // Session value, not the original lexical binding or its closures.
        else bindings[name]={read:()=>value,write:null};
      }});
    }
    bindings[name]=cell;
  }
  function prepare(names) {
    for(let i=0;i<names.length;i++) {
      const name=names[i];
      if(name==="tools"||name==="runtime"||(!own(bindings,name)&&own(globals,name)&&!descriptor(globals,name).configurable))
        throw new SyntaxErrorType("Identifier '"+name+"' has already been declared");
    }
    const slots=create(null);
    return (name,read,write,onlyIfMissing)=>{
      // Var markers can be inside a block or catch scope. Always publish
      // the function-scoped slot registered before the script body runs.
      if(!read) {
        publish(name,slots[name]);
        return;
      }
      const cell={read,write};
      slots[name]=cell;
      if(!onlyIfMissing||!own(bindings,name)) publish(name,cell);
    };
  }
  define(globals,"runtime",{value:freeze({state:()=>({generation:state.generation,execution:state.execution})})});
  const noop=()=>{};
  define(globals,"console",{value:freeze({log:noop,warn:noop,error:noop}),configurable:true});
  return freeze({
    configure(nextLimits,names,nextState){limits=nextLimits;state=nextState;install(names);},
    prepare,
    parse,
    encode:value=>encode(value===undefined?null:value,limits.output_bytes,true)
  });
})
