"use client";

import { toast } from "sonner";

// Module-owned workers survive closing the dialog and changing SPA pages.
const active = new Map();

export function startFolderImport({ id, name, files }) {
  if (files.length > 50000) throw new Error("导入文件数量超过 50000 个");
  if (id && active.has(id)) throw new Error("仓库正在导入");
  const key = id || Symbol();
  const worker = new Worker(new URL("../workers/capability-import.js", import.meta.url));
  const toastID = toast.loading(`${name}：准备文件`);
  active.set(key, worker);
  let offset = 0;
  const finish = (error) => {
    worker.terminate();
    active.delete(key);
    if (error) toast.error(`${name}：${error}`, { id: toastID });
    else toast.success(`${name}：上传完成`, { id: toastID });
  };
  const next = () => {
    try {
      if (offset >= files.length) { worker.postMessage({ type: "upload" }); return; }
      const batch = [];
      const end = Math.min(offset + 128, files.length);
      for (; offset < end; offset++) {
        const file = files[offset];
        batch.push({ file, path: file.webkitRelativePath });
      }
      worker.postMessage({ type: "batch", files: batch });
    } catch { finish("文件准备失败，重新选择文件夹"); }
  };
  worker.onmessage = ({ data }) => {
    if (data.type === "next") next();
    else if (data.type === "progress") toast.loading(`${name}：${data.text}`, { id: toastID });
    else if (data.type === "error") finish(data.error);
    else if (data.type === "result") {
      if (data.status === 401) { finish("登录已失效"); location.reload(); }
      else finish(data.status === 202 ? null : data.error || "导入失败");
    }
  };
  worker.onerror = () => finish("文件准备失败，重新选择文件夹");
  worker.postMessage({ type: "start", target: `/api/capability-library/import?${new URLSearchParams({ name, id: id || "" })}`, totalFiles: files.length });
  next();
}
