import { resolve } from "node:path";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

// 三个页面：settings(index.html) / selector / overlay，各自对应一个 Tauri 窗口
export default defineConfig({
  plugins: [react()],
  clearScreen: false,
  // 不监听 Rust 编译产物，否则 Windows 上会报 EBUSY
  server: { port: 1420, strictPort: true, watch: { ignored: ["**/src-tauri/**"] } },
  build: {
    target: "es2022",
    rollupOptions: {
      input: {
        settings: resolve(import.meta.dirname, "index.html"),
        selector: resolve(import.meta.dirname, "selector.html"),
        overlay: resolve(import.meta.dirname, "overlay.html"),
      },
    },
  },
});
