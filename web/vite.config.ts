import path from "node:path"

import tailwindcss from "@tailwindcss/vite"
import react from "@vitejs/plugin-react"
import { defineConfig, loadEnv } from "vite"

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), "")
  const backendTarget = env.VITE_BACKEND_TARGET || "http://127.0.0.1:19090"

  return {
    plugins: [react(), tailwindcss()],
    resolve: {
      alias: {
        "@": path.resolve(process.cwd(), "src"),
      },
    },
    server: {
      strictPort: true,
      proxy: {
        "/api": {
          target: backendTarget,
          changeOrigin: false,
          // Required for the v2 in-browser terminal (/api/v1/terminal/ws).
          ws: true,
        },
        "/health": {
          target: backendTarget,
          changeOrigin: false,
        },
      },
    },
  }
})
