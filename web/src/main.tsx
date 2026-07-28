import { StrictMode } from "react"
import { createRoot } from "react-dom/client"

import App from "@/App"
import "@/index.css"

const storedTheme = localStorage.getItem("hx-theme")
const prefersDark = window.matchMedia("(prefers-color-scheme: dark)").matches
document.documentElement.classList.toggle("dark", storedTheme === "dark" || (storedTheme !== "light" && prefersDark))

const root = document.getElementById("root")
if (!root) {
  throw new Error("root element is missing")
}

createRoot(root).render(
  <StrictMode>
    <App />
  </StrictMode>,
)
