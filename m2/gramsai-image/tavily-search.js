#!/usr/bin/env node
const https = require("https");
const dns = require("dns");
const KEY = process.env.TAVILY_API_KEY;
function rpc(o){ process.stdout.write(JSON.stringify(o)+"\n"); }
function resolveHost(host){
  return new Promise((resolve,reject)=>{
    dns.resolve4(host,(e,addrs)=>{
      if(e||!addrs||!addrs.length) return reject(new Error("dns resolve failed: "+(e&&e.code)));
      resolve(addrs[0]);
    });
  });
}
async function tavily(query){
  const ip = await resolveHost("api.tavily.com");
  const body = JSON.stringify({ api_key:KEY, query, search_depth:"advanced", max_results:6, include_answer:false, include_raw_content:true });
  return new Promise((resolve,reject)=>{
    const req = https.request({
      host: ip, servername:"api.tavily.com", port:443, path:"/search", method:"POST",
      headers:{"Host":"api.tavily.com","Content-Type":"application/json","Content-Length":Buffer.byteLength(body)}
    }, res=>{let d="";res.on("data",c=>d+=c);res.on("end",()=>resolve(d));});
    req.on("error",reject); req.setTimeout(25000,()=>req.destroy(new Error("timeout"))); req.write(body); req.end();
  });
}
function format(raw){
  let data; try{data=JSON.parse(raw);}catch{return "search error: bad response";}
  const results=(data.results||[]).slice(0,6);
  if(!results.length) return "No results found.";
  const docs=results.map((r,i)=>{
    const title=(r.title||("Result "+(i+1))).trim();
    const url=(r.url||"").trim();
    const content=((r.raw_content||r.content||"")+"").slice(0,9000);
    return "["+(i+1)+"] "+title+"\nURL: "+url+"\nCONTENT:\n"+content;
  });
  return "TOP WEB PAGES (use these as the factual basis):\n\n"+docs.join("\n\n");
}

// Auto-open the top Tavily result in the live browser panel (non-blocking).
// If the bridge is down or slow, this is ignored and search still works normally.
const http = require("http");
function pingBridge(rawData){
  try{
    const data = JSON.parse(rawData);
    const top = (data.results || [])[0];
    if(!top || !top.url) return;
    const body = JSON.stringify({ url: top.url });
    const port = process.env.BRIDGE_PORT || "8088";
    const req = http.request({
      host: "127.0.0.1", port: port, path: "/browse", method: "POST",
      headers: { "Content-Type": "application/json", "Content-Length": Buffer.byteLength(body) }
    }, res => { res.on("data", ()=>{}); res.on("end", ()=>{}); });
    req.on("error", ()=>{});            // bridge down => ignore, search still works
    req.setTimeout(2000, ()=>req.destroy());
    req.write(body); req.end();
  }catch{ /* never break search */ }
}

let buf="";
process.stdin.on("data",async chunk=>{
  buf+=chunk; let i;
  while((i=buf.indexOf("\n"))>=0){
    const line=buf.slice(0,i); buf=buf.slice(i+1);
    if(!line.trim()) continue;
    let msg; try{msg=JSON.parse(line);}catch{continue;}
    if(msg.method==="initialize"){
      rpc({jsonrpc:"2.0",id:msg.id,result:{protocolVersion:"2024-11-05",capabilities:{tools:{}},serverInfo:{name:"tavily-search",version:"1.0.0"}}});
    } else if(msg.method==="tools/list"){
      rpc({jsonrpc:"2.0",id:msg.id,result:{tools:[{
        name:"web_search",
        description:"Search the web for current information on any topic. Returns ranked results with titles, URLs and full page content. Use for any research, news, fact-finding, or current-events query, and call multiple times with different queries for deeper research.",
        inputSchema:{type:"object",properties:{query:{type:"string",description:"The search query"}},required:["query"]}
      }]}});
    } else if(msg.method==="tools/call"){
      try{
        const q=msg.params.arguments.query;
        const data=await tavily(q);
        pingBridge(data); // show top result in live panel (non-blocking)
        rpc({jsonrpc:"2.0",id:msg.id,result:{content:[{type:"text",text:format(data)}]}});
      }catch(e){
        rpc({jsonrpc:"2.0",id:msg.id,result:{content:[{type:"text",text:"search error: "+e.message}],isError:true}});
      }
    } else if(msg.method && msg.id!==undefined){
      rpc({jsonrpc:"2.0",id:msg.id,error:{code:-32601,message:"Method not found"}});
    }
  }
});
