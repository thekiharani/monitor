import { useState } from "react";
import { Check, Copy } from "lucide-react";
import { cn } from "@/lib/utils";
import { HighlightedCode, highlightLanguage } from "@/components/common/HighlightedCode";

export type SampleLang =
  | "curl"
  | "typescript"
  | "python"
  | "go"
  | "java"
  | "php"
  | "csharp"
  | "ruby"
  | "json";

export type CodeSamples = Partial<Record<SampleLang, string>>;

const LANG_LABELS: Record<SampleLang, string> = {
  curl: "cURL",
  typescript: "TypeScript",
  python: "Python",
  go: "Go",
  java: "Java",
  php: "PHP",
  csharp: "C#",
  ruby: "Ruby",
  json: "JSON",
};

const LANG_ORDER: SampleLang[] = [
  "curl",
  "typescript",
  "python",
  "go",
  "java",
  "php",
  "csharp",
  "ruby",
  "json",
];

interface CodeBlockProps {
  samples: CodeSamples;
  title?: string;
  className?: string;
}

export function CodeBlock({ samples, title, className }: CodeBlockProps) {
  const available = LANG_ORDER.filter((lang) => samples[lang] !== undefined);
  const [selected, setSelected] = useState<SampleLang>(available[0] ?? "curl");
  const [copied, setCopied] = useState(false);

  const code = samples[selected] ?? "";
  const lang = highlightLanguage(selected);

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(code);
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    } catch {}
  };

  return (
    <div
      className={cn("overflow-hidden rounded-xl border border-border bg-card shadow-sm", className)}
    >
      <div className="flex items-center justify-between gap-3 border-b border-border bg-muted/30 px-4 py-2">
        <div className="flex min-w-0 items-center gap-1 overflow-x-auto">
          {available.map((option) => (
            <button
              key={option}
              type="button"
              onClick={() => setSelected(option)}
              className={cn(
                "rounded-lg px-2.5 py-1 text-xs font-semibold transition-colors",
                selected === option
                  ? "bg-primary/10 text-primary"
                  : "text-muted-foreground hover:text-foreground",
              )}
            >
              {LANG_LABELS[option]}
            </button>
          ))}
        </div>
        <div className="flex shrink-0 items-center gap-2">
          {title && (
            <span className="hidden font-mono text-2xs text-muted-foreground sm:inline">
              {title}
            </span>
          )}
          <button
            type="button"
            onClick={copy}
            className="flex items-center gap-1.5 rounded-lg px-2 py-1 text-xs font-medium text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
          >
            {copied ? <Check className="size-3.5 text-success" /> : <Copy className="size-3.5" />}
            {copied ? "Copied" : "Copy"}
          </button>
        </div>
      </div>
      <div className="code-highlight">
        <HighlightedCode code={code} language={lang} />
      </div>
    </div>
  );
}
