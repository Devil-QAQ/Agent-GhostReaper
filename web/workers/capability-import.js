// File handles arrive in small batches. Multipart assembly and upload stay off
// the page's main thread; no file contents are copied into JavaScript buffers.
let body;
let target;
let totalFiles = 0;
let prepared = 0;
let lastProgress = 0;

self.onmessage = ({ data }) => {
  try {
    if (data.type === "start") {
      body = new FormData();
      target = data.target;
      totalFiles = data.totalFiles;
    } else if (data.type === "batch") {
      for (const { file, path } of data.files) body.append("files", file, path);
      prepared += data.files.length;
      if (Date.now() - lastProgress >= 150) {
        self.postMessage({ type: "progress", text: `准备文件 ${prepared}/${totalFiles}` });
        lastProgress = Date.now();
      }
      // Acknowledgements bound both queued file handles and main-thread work.
      self.postMessage({ type: "next" });
    } else if (data.type === "upload") {
      self.postMessage({ type: "progress", text: "上传文件" });
      const request = new XMLHttpRequest();
      request.open("POST", target);
      request.upload.onprogress = (event) => {
        if (Date.now() - lastProgress < 150) return;
        lastProgress = Date.now();
        const text = event.lengthComputable ? `上传文件 ${Math.floor(event.loaded / event.total * 100)}%` : `已上传 ${event.loaded} 字节`;
        self.postMessage({ type: "progress", text });
      };
      request.upload.onload = () => self.postMessage({ type: "progress", text: "整理文件" });
      request.onload = () => {
        let response = {};
        try { response = JSON.parse(request.responseText); } catch {}
        self.postMessage({ type: "result", status: request.status, error: response.error });
      };
      request.onerror = () => self.postMessage({ type: "error", error: "上传中断，重新导入" });
      request.send(body);
      body = null;
    }
  } catch {
    self.postMessage({ type: "error", error: "文件准备失败，重新选择文件夹" });
  }
};
