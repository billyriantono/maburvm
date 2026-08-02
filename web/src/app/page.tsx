import { Button } from "@/components/ui/button";

export default function Home() {
  return (
    <main className="min-h-screen bg-background p-8">
      {/* Header */}
      <header className="mb-12">
        <h1 className="text-4xl font-bold tracking-tight mb-2 text-foreground">
          MaburVM
        </h1>
        <p className="text-lg text-muted-foreground">
          Design System
        </p>
      </header>

      {/* Color Palette */}
      <section className="mb-8">
        <h2 className="text-2xl font-semibold mb-6">Color Palette</h2>
        <div className="grid grid-cols-2 md:grid-cols-4 gap-4">
          <div className="rounded-lg border bg-card p-4 shadow-sm">
            <div className="h-10 w-full rounded-md bg-primary mb-3" />
            <span className="font-medium block">Primary</span>
            <code className="text-sm text-muted-foreground">--primary</code>
          </div>
          <div className="rounded-lg border bg-card p-4 shadow-sm">
            <div className="h-10 w-full rounded-md bg-secondary mb-3" />
            <span className="font-medium block">Secondary</span>
            <code className="text-sm text-muted-foreground">--secondary</code>
          </div>
          <div className="rounded-lg border bg-card p-4 shadow-sm">
            <div className="h-10 w-full rounded-md bg-accent mb-3" />
            <span className="font-medium block">Accent</span>
            <code className="text-sm text-muted-foreground">--accent</code>
          </div>
          <div className="rounded-lg border bg-card p-4 shadow-sm">
            <div className="h-10 w-full rounded-md bg-emerald-500 mb-3" />
            <span className="font-medium block">Success</span>
            <code className="text-sm text-muted-foreground">emerald</code>
          </div>
          <div className="rounded-lg border bg-card p-4 shadow-sm">
            <div className="h-10 w-full rounded-md bg-destructive mb-3" />
            <span className="font-medium block">Danger</span>
            <code className="text-sm text-muted-foreground">--destructive</code>
          </div>
          <div className="rounded-lg border bg-card p-4 shadow-sm">
            <div className="h-10 w-full rounded-md bg-amber-500 mb-3" />
            <span className="font-medium block">Warning</span>
            <code className="text-sm text-muted-foreground">amber</code>
          </div>
          <div className="rounded-lg border bg-card p-4 shadow-sm">
            <div className="h-10 w-full rounded-md bg-muted mb-3" />
            <span className="font-medium block">Muted</span>
            <code className="text-sm text-muted-foreground">--muted</code>
          </div>
          <div className="rounded-lg border bg-card p-4 shadow-sm">
            <div className="h-10 w-full rounded-md bg-foreground mb-3" />
            <span className="font-medium block">Foreground</span>
            <code className="text-sm text-muted-foreground">--foreground</code>
          </div>
        </div>
      </section>

      {/* Buttons */}
      <section className="mb-8">
        <h2 className="text-2xl font-semibold mb-6">Buttons</h2>

        <div className="space-y-6">
          <div>
            <h3 className="text-base font-medium mb-4">Variants</h3>
            <div className="flex flex-wrap gap-4">
              <Button variant="default">Default</Button>
              <Button variant="secondary">Secondary</Button>
              <Button variant="destructive">Danger</Button>
              <Button variant="ghost">Ghost</Button>
              <Button variant="outline">Outline</Button>
            </div>
          </div>

          <div>
            <h3 className="text-base font-medium mb-4">Sizes</h3>
            <div className="flex flex-wrap gap-4 items-center">
              <Button size="sm">Small</Button>
              <Button size="default">Default</Button>
              <Button size="lg">Large</Button>
            </div>
          </div>

          <div>
            <h3 className="text-base font-medium mb-4">States</h3>
            <div className="flex flex-wrap gap-4">
              <Button>Normal</Button>
              <Button disabled>Disabled</Button>
            </div>
          </div>
        </div>
      </section>

      {/* Cards */}
      <section className="mb-8">
        <h2 className="text-2xl font-semibold mb-6">Cards</h2>

        <div className="grid md:grid-cols-3 gap-6">
          <div className="rounded-lg border bg-card text-card-foreground p-6 shadow-sm">
            <h3 className="text-lg font-semibold mb-2">Basic Card</h3>
            <p className="text-sm text-muted-foreground">
              Rounded corners, subtle border and soft shadow.
            </p>
          </div>

          <div className="rounded-lg border bg-card text-card-foreground p-6 shadow-sm">
            <h3 className="text-lg font-semibold mb-2">Content Card</h3>
            <p className="text-sm text-muted-foreground">
              Neutral surface for primary content.
            </p>
          </div>

          <div className="rounded-lg border bg-muted text-foreground p-6 shadow-sm">
            <h3 className="text-lg font-semibold mb-2">Muted Card</h3>
            <p className="text-sm text-muted-foreground">
              Muted surface for secondary info.
            </p>
          </div>
        </div>
      </section>

      {/* Input */}
      <section className="mb-8">
        <h2 className="text-2xl font-semibold mb-6">Input</h2>

        <div className="max-w-md space-y-4">
          <div>
            <label htmlFor="text-input" className="block text-sm font-medium mb-2">
              Text Input
            </label>
            <input
              id="text-input"
              type="text"
              className="flex h-10 w-full rounded-md border border-input bg-background px-3 py-2 text-sm placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2"
              placeholder="Enter text..."
            />
          </div>

          <div>
            <label htmlFor="password-input" className="block text-sm font-medium mb-2">
              Password
            </label>
            <input
              id="password-input"
              type="password"
              className="flex h-10 w-full rounded-md border border-input bg-background px-3 py-2 text-sm placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2"
              placeholder="••••••••"
            />
          </div>
        </div>
      </section>

      {/* Badges */}
      <section className="mb-8">
        <h2 className="text-2xl font-semibold mb-6">Badges</h2>

        <div className="flex flex-wrap gap-4">
          <span className="inline-flex items-center rounded-md border px-2.5 py-0.5 text-xs font-medium">Default</span>
          <span className="inline-flex items-center rounded-md bg-muted text-muted-foreground px-2.5 py-0.5 text-xs font-medium">Secondary</span>
          <span className="inline-flex items-center rounded-md border border-destructive/20 bg-destructive/10 text-destructive px-2.5 py-0.5 text-xs font-medium">Danger</span>
          <span className="inline-flex items-center rounded-md border border-emerald-200 bg-emerald-50 text-emerald-700 px-2.5 py-0.5 text-xs font-medium dark:border-emerald-900 dark:bg-emerald-950 dark:text-emerald-400">Success</span>
          <span className="inline-flex items-center rounded-md border border-amber-200 bg-amber-50 text-amber-700 px-2.5 py-0.5 text-xs font-medium dark:border-amber-900 dark:bg-amber-950 dark:text-amber-400">Warning</span>
        </div>
      </section>
    </main>
  );
}
