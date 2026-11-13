import type { Metadata } from "next";
import "./globals.css";
import { cn } from "@/lib/utils";
import { QueryProvider } from "@/lib/query-provider";
import { ConfirmProvider } from "@/components/confirm-provider";
import { BrandingProvider } from "@/lib/branding/provider";
import { type Branding, faviconOf, themeVars, withDefaults } from "@/lib/branding/core";
import { brandFont, monoFont } from "@/lib/branding/font";
import { ThemeProvider } from "next-themes";
import { Toaster } from "sonner";

// Read on the server for every request, so the first paint already carries the
// operator's name, colour and icon. It goes through this app's own /api
// rewrite, which is the one address that is right in every deployment; an
// unreachable panel just means the page renders unbranded.
async function loadBranding(): Promise<Branding> {
  try {
    const base = process.env.BRANDING_API_URL ?? `http://127.0.0.1:${process.env.PORT ?? 3000}`;
    const res = await fetch(`${base}/api/v1/branding`, { cache: "no-store", signal: AbortSignal.timeout(2000) });
    if (!res.ok) return withDefaults(null);
    const body = await res.json();
    return withDefaults(body?.data);
  } catch {
    return withDefaults(null);
  }
}

export async function generateMetadata(): Promise<Metadata> {
  const b = await loadBranding();
  return {
    title: b.name,
    description: b.tagline || "Virtual machine management",
    icons: { icon: faviconOf(b) },
  };
}

export default async function RootLayout({
  children,
}: Readonly<{
  children: React.ReactNode;
}>) {
  const branding = await loadBranding();
  return (
    // suppressHydrationWarning: the theme class is set on <html> before React
    // loads, so the server and first client render differ there by design.
    <html lang="en" suppressHydrationWarning className={cn("font-sans", brandFont.variable, monoFont.variable)} data-brand={Object.keys(themeVars(branding)).length ? "" : undefined} style={themeVars(branding) as React.CSSProperties}>
      <body className="font-sans">
        <ThemeProvider attribute="class" defaultTheme="system" enableSystem disableTransitionOnChange>
          <QueryProvider>
            <BrandingProvider initial={branding}>
              <ConfirmProvider>{children}</ConfirmProvider>
            </BrandingProvider>
          </QueryProvider>
          <Toaster richColors position="top-right" />
        </ThemeProvider>
      </body>
    </html>
  );
}
