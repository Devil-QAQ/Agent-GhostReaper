import path from "node:path";
import { fileURLToPath } from "node:url";

/** @type {import('next').NextConfig} */
const nextConfig = {
  // 静态导出,产物 out/ 由 Makefile 拷入 internal/webui/dist 后 go:embed 进单二进制
  output: "export",
  // 锁定 workspace root 为本目录,避免 Next 误推到上级(家目录存在其他 lockfile)
  outputFileTracingRoot: path.dirname(fileURLToPath(import.meta.url)),
};

export default nextConfig;
