import React from "react";
import ReactDOM from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { BrowserRouter } from "react-router-dom";
import { ThemeProvider } from "@/context/theme-context";
import { Toaster } from "@/components/ui/sonner";
import { App } from "@/App";
import { ErrorBoundary, RootErrorFallback } from "@/components/common/ErrorBoundary";
import { APIError } from "@/api";
import "@/index.css";

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 15_000,
      retry: (failureCount, error) => {
        if (error instanceof APIError && error.status < 500) return false;
        return failureCount < 1;
      },
      retryDelay: 500,
    },
    mutations: { retry: false },
  },
});

ReactDOM.createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <ErrorBoundary fallback={<RootErrorFallback />}>
      <QueryClientProvider client={queryClient}>
        <ThemeProvider defaultTheme="system">
          <BrowserRouter>
            <App />
            <Toaster richColors />
          </BrowserRouter>
        </ThemeProvider>
      </QueryClientProvider>
    </ErrorBoundary>
  </React.StrictMode>,
);
