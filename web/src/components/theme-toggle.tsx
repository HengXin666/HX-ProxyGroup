import { useEffect, useState } from "react"
import { Monitor, Moon, Sun } from "lucide-react"

import { Button } from "@/components/ui/button"

type Theme = "light" | "dark" | "system"

function savedTheme(): Theme {
  const value = localStorage.getItem("hx-theme")
  return value === "light" || value === "dark" ? value : "system"
}

function applyTheme(theme: Theme) {
  const dark = theme === "dark" || (theme === "system" && window.matchMedia("(prefers-color-scheme: dark)").matches)
  document.documentElement.classList.toggle("dark", dark)
  if (theme === "system") localStorage.removeItem("hx-theme")
  else localStorage.setItem("hx-theme", theme)
}

export function ThemeToggle() {
  const [theme, setTheme] = useState<Theme>(savedTheme)

  useEffect(() => {
    applyTheme(theme)
    const media = window.matchMedia("(prefers-color-scheme: dark)")
    const update = () => { if (theme === "system") applyTheme("system") }
    media.addEventListener("change", update)
    return () => media.removeEventListener("change", update)
  }, [theme])

  const next: Theme = theme === "system" ? "light" : theme === "light" ? "dark" : "system"
  const Icon = theme === "light" ? Sun : theme === "dark" ? Moon : Monitor
  const label = theme === "light" ? "明亮主题" : theme === "dark" ? "黑夜主题" : "跟随系统"
  return <Button variant="ghost" size="icon" title={`${label}，点击切换`} aria-label={`${label}，点击切换`} onClick={() => setTheme(next)}><Icon /></Button>
}
