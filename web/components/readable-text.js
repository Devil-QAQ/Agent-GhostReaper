"use client";

import * as React from "react";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";

const { Children } = React;

// 面向人的正文统一按段落阅读。保留完整内容，不按字数裁句；报文与命令
// 由各自证据组件呈现，不在这里改写。renderText 用于保留节点引用交互。
export function ReadableText({ text, renderText, muted = false, className = "" }) {
  if (!text) return null;
  // 引用只装饰正文文本，不改代码、链接或证据；Markdown 负责结构，
  // 不能先按空行切开，否则列表、代码块与段落的层级会丢失。
  const decorate = children => Children.map(children, child => {
    if (typeof child === "string") return renderText ? renderText(child) : child;
    if (!React.isValidElement(child) || typeof child.type !== "string" || ["code", "pre", "a"].includes(child.type)) return child;
    return child.props.children ? React.cloneElement(child, {}, decorate(child.props.children)) : child;
  });
  const block = (tag, styles) => function TextBlock({ children }) {
    return React.createElement(tag, { className: styles }, decorate(children));
  };
  return <div className={`space-y-3 whitespace-normal text-sm leading-relaxed [overflow-wrap:anywhere] ${muted ? "text-muted-foreground" : "text-foreground"} ${className}`}>
    <ReactMarkdown remarkPlugins={[remarkGfm]} components={{
      p: block("p", "whitespace-pre-line"),
      h1: block("h2", "t-sec font-semibold"),
      h2: block("h2", "t-sec font-semibold"),
      h3: block("h3", "font-semibold"),
      h4: block("h4", "font-semibold"),
      h5: block("h5", "font-semibold"),
      h6: block("h6", "font-semibold"),
      ul: ({ children }) => <ul className="list-disc space-y-2 pl-5">{children}</ul>,
      ol: ({ children }) => <ol className="list-decimal space-y-2 pl-5">{children}</ol>,
      li: block("li", "space-y-2"),
      blockquote: ({ children }) => <blockquote className="border-l border-border pl-3 text-muted-foreground">{children}</blockquote>,
      pre: ({ children }) => <pre className="max-h-80 overflow-auto rounded-md bg-muted p-3 whitespace-pre text-xs">{children}</pre>,
      code: ({ children }) => <code className="num text-xs">{children}</code>,
      a: ({ href, children }) => <a href={href} target="_blank" rel="noopener noreferrer" className="text-primary underline underline-offset-2">{children}</a>,
      table: ({ children }) => <div className="overflow-x-auto"><table className="w-full text-left">{children}</table></div>,
      th: block("th", "px-3 py-2 font-semibold"),
      td: block("td", "px-3 py-2 align-top"),
    }}>{String(text)}</ReactMarkdown>
  </div>;
}
