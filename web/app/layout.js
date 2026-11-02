import "./globals.css";
import { Toaster } from "@/components/ui/sonner";

export const metadata = {
  title: "Agent GhostReaper",
  description: "LLM 多 Agent 驱动的自主渗透测试系统(仅供授权测试使用)",
  icons: {
    icon: [
      { url: "/favicon.ico", sizes: "any" },
      { url: "/icon.svg", type: "image/svg+xml" },
      { url: "/favicon-64.png", sizes: "64x64", type: "image/png" },
    ],
  },
};

// 主题防闪烁 boot script(P3-10;P4-26 起缺省=跟随系统):首帧渲染前读
// gr.theme(未设置视为 system)并落 .light class——亮色首开不再"先暗后亮"
// 闪一下。与 lib/theme.js 的 applyThemeClass 是同一逻辑的内联副本(此处
// 不能 import client 模块)。
const THEME_BOOT = `(function(){try{var t=localStorage.getItem("gr.theme");if(t!=="dark"&&t!=="light")t="system";var l=t==="light"||(t==="system"&&window.matchMedia("(prefers-color-scheme: light)").matches);document.documentElement.classList.toggle("light",l);}catch(e){}})();`;

export default function RootLayout({ children }) {
  return (
    // suppressHydrationWarning:boot script 先于 React 改写 <html> class,
    // 属预期的预渲染/客户端不一致点(仅本标签)。
    <html lang="zh-CN" suppressHydrationWarning>
      <head>
        <script dangerouslySetInnerHTML={{ __html: THEME_BOOT }} />
      </head>
      <body className="min-h-screen antialiased">
        {children}
        <Toaster />
      </body>
    </html>
  );
}
