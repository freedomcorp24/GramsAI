#!/usr/bin/env node
// image-gen MCP server: generate_image. Calls the GramsAI gateway (model
// "Image Gen"), writes the PNG into the workspace under images/, and returns a
// SHORT markdown image pointing at /dl?dir=images/<name>. The short string
// survives opencode's tool-output storage (a 5MB data URL does not), renders
// inline via the markdown renderer, and is downloadable via the same /dl route.
const http = require("http");
const fs = require("fs");
const path = require("path");
const { execFile } = require("child_process");
const { URL } = require("url");
const TOKEN = process.env.GRAMSAI_TOKEN || "";
const GATEWAY = process.env.GRAMSAI_GATEWAY_V1 || "http://10.152.152.111/v1";
const WORKSPACE = process.cwd();  // session worktree (opencode sets cwd per session)
function rpc(o){ process.stdout.write(JSON.stringify(o)+"\n"); }
function generate(prompt, aspect){
  const u = new URL(GATEWAY.replace(/\/+$/,"") + "/chat/completions");
  const payload = { model:"Image Gen", stream:false, messages:[{role:"user",content:prompt}], modalities:["image"] };
  if (aspect) payload.image_config = { aspect_ratio: aspect };
  const body = JSON.stringify(payload);
  return new Promise((resolve,reject)=>{
    const req = http.request({
      host:u.hostname, port:u.port||80, path:u.pathname, method:"POST",
      headers:{ "Content-Type":"application/json", "Content-Length":Buffer.byteLength(body), "Authorization":"Bearer "+TOKEN }
    }, res=>{ let d=""; res.on("data",c=>d+=c); res.on("end",()=>resolve(d)); });
    req.on("error",reject);
    req.setTimeout(120000, ()=>req.destroy(new Error("image generation timeout")));
    req.write(body); req.end();
  });
}
function parse(raw){
  let data; try{ data=JSON.parse(raw); }catch{ return {error:"bad gateway response"}; }
  if (data.error) return { error:(data.error.message||"gateway error") };
  const msg = ((data.choices||[])[0]||{}).message || {};
  const content = msg.content || "";
  const m = /\(data:([^;]+);base64,([^)]+)\)/.exec(content);
  if (!m) return { error:"no image in response" };
  return { mime:m[1], b64:m[2] };
}
function slug(s){ return (s||"image").toLowerCase().replace(/[^a-z0-9]+/g,"-").replace(/^-+|-+$/g,"").slice(0,40)||"image"; }
let buf="";
process.stdin.on("data", async chunk=>{
  buf+=chunk; let i;
  while((i=buf.indexOf("\n"))>=0){
    const line=buf.slice(0,i); buf=buf.slice(i+1);
    if(!line.trim()) continue;
    let msg; try{ msg=JSON.parse(line); }catch{ continue; }
    if(msg.method==="initialize"){
      rpc({jsonrpc:"2.0",id:msg.id,result:{protocolVersion:"2024-11-05",capabilities:{tools:{}},serverInfo:{name:"image-gen",version:"6.0.0"}}});
    } else if(msg.method==="tools/list"){
      rpc({jsonrpc:"2.0",id:msg.id,result:{tools:[{
        name:"generate_image",
        description:"Generate an image from a text description. Use whenever the user asks to create, draw, generate, paint, or make a picture/image/art/logo/illustration. The generated image is displayed automatically and is downloadable. Do NOT call read/glob/ls. Provide a detailed visual prompt.",
        inputSchema:{type:"object",properties:{
          prompt:{type:"string",description:"Detailed description of the image to generate"},
          aspect_ratio:{type:"string",description:"Optional aspect ratio like '1:1','16:9','9:16','4:3'"}
        },required:["prompt"]}
      }]}});
    } else if(msg.method==="tools/call"){
      try{
        const args=msg.params.arguments||{};
        const prompt=args.prompt;
        if(!prompt){ rpc({jsonrpc:"2.0",id:msg.id,result:{content:[{type:"text",text:"image error: no prompt"}],isError:true}}); continue; }
        const raw=await generate(prompt,args.aspect_ratio);
        const r=parse(raw);
        if(r.error){
          rpc({jsonrpc:"2.0",id:msg.id,result:{content:[{type:"text",text:"image generation failed: "+r.error}],isError:true}});
        } else {
          const ext=(r.mime.split("/")[1]||"png").replace("jpeg","jpg");
          const fname=slug(prompt)+"-"+Date.now()+"."+ext;
          const rel="images/"+fname;
          const full=path.join(WORKSPACE, rel);
          fs.mkdirSync(path.dirname(full),{recursive:true});
          fs.writeFileSync(full, Buffer.from(r.b64,"base64"));
          // inline image via /dl resolved by chat id server-side
          const url="/dl?dir="+encodeURIComponent(WORKSPACE+"/"+rel);
          const md="!["+prompt.replace(/[\[\]]/g,"").slice(0,80)+"]("+url+")";
          rpc({jsonrpc:"2.0",id:msg.id,result:{content:[{type:"text",text:md}]}});
        }
      }catch(e){
        rpc({jsonrpc:"2.0",id:msg.id,result:{content:[{type:"text",text:"image generation error: "+e.message}],isError:true}});
      }
    } else if(msg.method && msg.id!==undefined){
      rpc({jsonrpc:"2.0",id:msg.id,error:{code:-32601,message:"Method not found"}});
    }
  }
});
