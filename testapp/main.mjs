// This is a test node.js app that shows html on response
// const http = require("http");
import http from "http";
const port = 9999;

const server = http.createServer(function(_, res) {
  res.writeHead(200, { "content-type": "text/html" });
  const htmlContent = `
    <!DOCTYPE html>
    <html lang="en">
    <head>
      <meta charset="UTF-8">
      <meta name="viewport" content="width=device-width, initial-scale=1.0">
      <title>Tunneling Test: update!</title>
    </head>
    <body>
      <h1>Tunneling Test</h1>
    </body>
    </html>
  `;
  res.end(htmlContent);
});

server.listen(port, function() {
  console.log("server is running on port " + port)
});

