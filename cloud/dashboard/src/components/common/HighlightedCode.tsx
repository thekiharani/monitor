import { useMemo } from "react";
import hljs from "highlight.js/lib/core";
import bash from "highlight.js/lib/languages/bash";
import json from "highlight.js/lib/languages/json";
import javascript from "highlight.js/lib/languages/javascript";
import typescript from "highlight.js/lib/languages/typescript";
import python from "highlight.js/lib/languages/python";
import go from "highlight.js/lib/languages/go";
import java from "highlight.js/lib/languages/java";
import php from "highlight.js/lib/languages/php";
import csharp from "highlight.js/lib/languages/csharp";
import ruby from "highlight.js/lib/languages/ruby";
import xml from "highlight.js/lib/languages/xml";

hljs.registerLanguage("bash", bash);
hljs.registerLanguage("json", json);
hljs.registerLanguage("javascript", javascript);
hljs.registerLanguage("typescript", typescript);
hljs.registerLanguage("python", python);
hljs.registerLanguage("go", go);
hljs.registerLanguage("java", java);
hljs.registerLanguage("php", php);
hljs.registerLanguage("csharp", csharp);
hljs.registerLanguage("ruby", ruby);
hljs.registerLanguage("xml", xml);

const GRAMMAR: Record<string, string> = {
  curl: "bash",
  bash: "bash",
  shell: "bash",
  json: "json",
  javascript: "javascript",
  typescript: "typescript",
  ts: "typescript",
  python: "python",
  py: "python",
  go: "go",
  java: "java",
  php: "php",
  csharp: "csharp",
  ruby: "ruby",
};

export function highlightLanguage(lang: string): string {
  return GRAMMAR[lang] ?? "plaintext";
}

export function HighlightedCode({
  code,
  language = "plaintext",
  className,
}: {
  code: string;
  language?: string;
  className?: string;
}) {
  const lang = GRAMMAR[language] ?? "plaintext";
  const html = useMemo(() => {
    if (lang === "plaintext") return escapeHtml(code);
    try {
      return hljs.highlight(code, { language: lang, ignoreIllegals: true }).value;
    } catch {
      return escapeHtml(code);
    }
  }, [code, lang]);

  return (
    <pre
      className={`overflow-x-auto p-4 font-mono text-xs leading-relaxed ${className ?? ""}`}
      aria-label={lang === "plaintext" ? "code" : `${lang} code`}
    >
      <code className={`hljs language-${lang}`} dangerouslySetInnerHTML={{ __html: html }} />
    </pre>
  );
}

function escapeHtml(input: string): string {
  return input.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
}
