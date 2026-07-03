import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// Dev server proxies /api -> the query-service, so the browser never makes a
// cross-origin request and query-service's WithAuth middleware / any future
// CORS policy never needs to change for local development.
// Override the target with QUERY_SERVICE_URL if query-service isn't on the
// default localhost:8080 (matches the env var cmd/mcp-server already uses).
export default defineConfig({
  plugins: [react()],
  server: {
    proxy: {
      "/api": {
        target: process.env.QUERY_SERVICE_URL ?? "http://localhost:8080",
        changeOrigin: true,
        rewrite: (path) => path.replace(/^\/api/, ""),
      },
    },
  },
});
