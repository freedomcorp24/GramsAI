#!/usr/bin/env node
// memory-search MCP server: exposes search_memory, which queries the user's past
// conversation episodes via the GramsAI gateway (M1) /api/memory/search.
// Auth: the container's GRAMSAI_TOKEN (same token used for LLM calls).
// Modeled on tavily-search.js (JSON-RPC over stdio).
const http = require("http");

const TOKEN = process.env.GRAMSAI_TOKEN || "";
// Gateway base: same host the LLM provider uses (http://10.152.152.111/v1).
// Derive the bare host; default to the known gateway IP.
const GATEWAY = (process.env.GRAMSAI_GATEWAY_V1 || "http://10.152.152.111/v1").replace(/\/v1\/?$/, "");

function rpc(o){ process.stdout.write(JSON.stringify(o)+"\n"); }

function searchMemory(query, k){
  const body = JSON.stringify({ query, k: k || 5 });
  const u = new URL(GATEWAY + "/api/memory/search");
  return new Promise((resolve,reject)=>{
    const req = http.request({
      host: u.hostname,
      port: u.port || 80,
      path: u.pathname,
      method: "POST",
      headers: {
        "Authorization": "Bearer " + TOKEN,
        "Content-Type": "application/json",
        "Content-Length": Buffer.byteLength(body)
      }
    }, res=>{ let d=""; res.on("data",c=>d+=c); res.on("end",()=>resolve(d)); });
    req.on("error",reject);
    req.setTimeout(15000,()=>req.destroy(new Error("timeout")));
    req.write(body); req.end();
  });
}

function format(raw){
  let data; try{ data=JSON.parse(raw); }catch{ return "memory search error: bad response"; }
  const results = (data.results||[]);
  if(!results.length) return "No relevant past conversations found.";
  const docs = results.map((r,i)=>{
    const when = (r.created_at||"").slice(0,10);
    return "["+(i+1)+"] ("+when+") "+(r.content||"").trim();
  });
  return "RELEVANT PAST CONVERSATIONS (from this user's memory):\n\n"+docs.join("\n\n");
}

let buf="";
process.stdin.on("data", async chunk=>{
  buf+=chunk; let i;
  while((i=buf.indexOf("\n"))>=0){
    const line=buf.slice(0,i); buf=buf.slice(i+1);
    if(!line.trim()) continue;
    let msg; try{msg=JSON.parse(line);}catch{continue;}
    if(msg.method==="initialize"){
      rpc({jsonrpc:"2.0",id:msg.id,result:{protocolVersion:"2024-11-05",capabilities:{tools:{}},serverInfo:{name:"memory-search",version:"1.0.0"}}});
    } else if(msg.method==="tools/list"){
      rpc({jsonrpc:"2.0",id:msg.id,result:{tools:[{
        name:"search_memory",
        description:"Search the user's own past conversations for relevant context. Use when the user refers to something discussed before (\"what did we decide about X\", \"the thing we talked about\", \"continue from last time\") or when prior context would help. Returns summaries of the most relevant past conversations.",
        inputSchema:{type:"object",properties:{query:{type:"string",description:"What to look for in past conversations"}},required:["query"]}
      }]}});
    } else if(msg.method==="tools/call"){
      try{
        const q=msg.params.arguments.query;
        const data=await searchMemory(q, 5);
        rpc({jsonrpc:"2.0",id:msg.id,result:{content:[{type:"text",text:format(data)}]}});
      }catch(e){
        rpc({jsonrpc:"2.0",id:msg.id,result:{content:[{type:"text",text:"memory search error: "+e.message}],isError:true}});
      }
    } else if(msg.method && msg.id!==undefined){
      rpc({jsonrpc:"2.0",id:msg.id,error:{code:-32601,message:"Method not found"}});
    }
  }
});
