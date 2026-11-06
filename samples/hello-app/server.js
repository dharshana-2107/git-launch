// A tiny web app to test Launchpad with. No dependencies: only Node's built-in http module.
const http = require("http");

const port = process.env.PORT || 3000;
const greeting = process.env.GREETING || "Hello from Launchpad!";

http
  .createServer((req, res) => {
    if (req.url === "/health") {
      res.writeHead(200, { "Content-Type": "application/json" });
      return res.end(JSON.stringify({ status: "ok" }));
    }
    res.writeHead(200, { "Content-Type": "text/html; charset=utf-8" });
    res.end(`<!doctype html>
<html><head><title>hello-app</title>
<style>body{font-family:system-ui,sans-serif;display:grid;place-items:center;height:100vh;margin:0;background:#0f172a;color:#e2e8f0}
h1{font-size:40px;margin:0}p{color:#94a3b8}</style></head>
<body><div><h1>${greeting}</h1><p>Served from a container built by Launchpad · ${new Date().toISOString()}</p></div></body></html>`);
  })
  .listen(port, () => console.log(`hello-app listening on ${port}`));
