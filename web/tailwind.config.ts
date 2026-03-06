import type { Config } from "tailwindcss";

const config: Config = {
  darkMode: ["class"],
  content: [
    "./src/app/**/*.{js,ts,jsx,tsx,mdx}",
    "./src/components/**/*.{js,ts,jsx,tsx,mdx}",
    "./app/**/*.{js,ts,jsx,tsx,mdx}",
    "./components/**/*.{js,ts,jsx,tsx,mdx}",
  ],
  theme: {
    container: {
      center: true,
      padding: "2rem",
      screens: {
        "2xl": "1400px",
      },
    },
    extend: {
      colors: {
        border: "hsl(var(--border))",
        input: "hsl(var(--input))",
        ring: "hsl(var(--ring))",
        background: "#F5F5F5",
        foreground: "#000000",
        primary: {
          DEFAULT: "#FFE500",
          foreground: "#000000",
        },
        secondary: {
          DEFAULT: "#00F0FF",
          foreground: "#000000",
        },
        destructive: {
          DEFAULT: "#FF4444",
          foreground: "#FFFFFF",
        },
        muted: {
          DEFAULT: "#E5E5E5",
          foreground: "#666666",
        },
        accent: {
          DEFAULT: "#FF00A0",
          foreground: "#FFFFFF",
        },
        popover: {
          DEFAULT: "#FFFFFF",
          foreground: "#000000",
        },
        card: {
          DEFAULT: "#FFFFFF",
          foreground: "#000000",
        },
        success: {
          DEFAULT: "#CCFF00",
          foreground: "#000000",
        },
        warning: {
          DEFAULT: "#FFAA00",
          foreground: "#000000",
        },
        danger: {
          DEFAULT: "#FF4444",
          foreground: "#FFFFFF",
        },
      },
      borderRadius: {
        DEFAULT: "0",
        lg: "0",
        md: "0",
        sm: "0",
        xl: "0",
        "2xl": "0",
        full: "0",
      },
      borderWidth: {
        DEFAULT: "2px",
        0: "0",
        1: "1px",
        2: "2px",
        3: "3px",
        4: "4px",
      },
      boxShadow: {
        neo: "4px 4px 0px 0px #000000",
        "neo-sm": "2px 2px 0px 0px #000000",
        "neo-lg": "6px 6px 0px 0px #000000",
        "neo-xl": "8px 8px 0px 0px #000000",
        "neo-hover": "6px 6px 0px 0px #000000",
        "neo-active": "2px 2px 0px 0px #000000",
        "neo-inset": "inset 4px 4px 0px 0px rgba(0,0,0,0.3)",
      },
      keyframes: {
        "neo-pulse": {
          "0%, 100%": { boxShadow: "4px 4px 0px 0px #000000" },
          "50%": { boxShadow: "8px 8px 0px 0px #000000" },
        },
        "neo-bounce": {
          "0%, 100%": { transform: "translateY(0)" },
          "50%": { transform: "translateY(-4px)" },
        },
        "accordion-down": {
          from: { height: "0" },
          to: { height: "var(--radix-accordion-content-height)" },
        },
        "accordion-up": {
          from: { height: "var(--radix-accordion-content-height)" },
          to: { height: "0" },
        },
      },
      animation: {
        "neo-pulse": "neo-pulse 2s ease-in-out infinite",
        "neo-bounce": "neo-bounce 0.3s ease-in-out",
        "accordion-down": "accordion-down 0.2s ease-out",
        "accordion-up": "accordion-up 0.2s ease-out",
      },
    },
  },
  plugins: [],
};

export default config;