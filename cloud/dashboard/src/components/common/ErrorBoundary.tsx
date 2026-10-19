import { Component, type ErrorInfo, type ReactNode } from "react";
import { ErrorPage } from "@/components/common/ErrorPage";

interface Props {
  children: ReactNode;
  fallback?: ReactNode;
  resetKey?: unknown;
}

interface State {
  error: Error | null;
  resetKey: unknown;
}

export function RootErrorFallback() {
  return (
    <div
      style={{
        maxWidth: "34rem",
        margin: "4rem auto",
        padding: "0 1.5rem",
        fontFamily: "system-ui, sans-serif",
        lineHeight: 1.6,
      }}
    >
      <h1 style={{ fontSize: "1.25rem", margin: "0 0 0.5rem" }}>The dashboard failed to start</h1>
      <p style={{ margin: "0 0 1rem", color: "#52525b" }}>
        Something went wrong before the page could render. Reloading usually clears it.
      </p>
      <button
        type="button"
        onClick={() => globalThis.location.reload()}
        style={{
          padding: "0.5rem 0.9rem",
          borderRadius: "0.5rem",
          border: "1px solid #d4d4d8",
          background: "transparent",
          cursor: "pointer",
          font: "inherit",
        }}
      >
        Reload
      </button>
    </div>
  );
}

export class ErrorBoundary extends Component<Props, State> {
  override state: State = { error: null, resetKey: this.props.resetKey };

  static getDerivedStateFromError(error: Error) {
    return { error };
  }

  static getDerivedStateFromProps(props: Props, state: State): Partial<State> | null {
    if (props.resetKey !== state.resetKey) {
      return { error: null, resetKey: props.resetKey };
    }
    return null;
  }

  override componentDidCatch(error: Error, info: ErrorInfo) {
    console.error("Unhandled render error", error, info.componentStack);
  }

  override render() {
    if (!this.state.error) return this.props.children;
    if (this.props.fallback) return this.props.fallback;
    return <ErrorPage code={500} />;
  }
}
