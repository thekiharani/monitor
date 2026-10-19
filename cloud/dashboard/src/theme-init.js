(function () {
  try {
    const stored = localStorage.getItem("noria-monitor-theme");
    const dark =
      stored === "dark" ||
      ((stored === "system" || stored === null) &&
        window.matchMedia("(prefers-color-scheme: dark)").matches);
    if (dark) document.documentElement.classList.add("dark");
  } catch {}
})();
