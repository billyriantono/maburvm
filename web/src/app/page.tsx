import { Button } from "@/components/ui/button";

export default function Home() {
  return (
    <main className="min-h-screen bg-background p-8">
      {/* Header */}
      <header className="mb-12">
        <h1 className="text-6xl font-black uppercase mb-4 tracking-tighter" style={{ textShadow: '4px 4px 0px #000' }}>
          MaburVM
        </h1>
        <p className="text-xl font-bold uppercase tracking-wide">
          Neo-Brutalist Design System
        </p>
      </header>

      {/* Color Palette */}
      <section className="neo-section mb-8">
        <h2 className="text-3xl font-black uppercase mb-6">Color Palette</h2>
        <div className="grid grid-cols-2 md:grid-cols-4 gap-4">
          <div className="bg-primary border-2 border-black p-4 shadow-neo">
            <span className="font-black uppercase block">Primary</span>
            <code className="text-sm">#FFE500</code>
          </div>
          <div className="bg-secondary border-2 border-black p-4 shadow-neo">
            <span className="font-black uppercase block">Secondary</span>
            <code className="text-sm">#00F0FF</code>
          </div>
          <div className="bg-accent border-2 border-black p-4 shadow-neo text-white">
            <span className="font-black uppercase block">Accent</span>
            <code className="text-sm">#FF00A0</code>
          </div>
          <div className="bg-success border-2 border-black p-4 shadow-neo">
            <span className="font-black uppercase block">Success</span>
            <code className="text-sm">#CCFF00</code>
          </div>
          <div className="bg-danger border-2 border-black p-4 shadow-neo text-white">
            <span className="font-black uppercase block">Danger</span>
            <code className="text-sm">#FF4444</code>
          </div>
          <div className="bg-warning border-2 border-black p-4 shadow-neo">
            <span className="font-black uppercase block">Warning</span>
            <code className="text-sm">#FFAA00</code>
          </div>
          <div className="bg-background border-2 border-black p-4 shadow-neo">
            <span className="font-black uppercase block">Background</span>
            <code className="text-sm">#F5F5F5</code>
          </div>
          <div className="bg-black border-2 border-black p-4 shadow-neo text-white">
            <span className="font-black uppercase block">Foreground</span>
            <code className="text-sm">#000000</code>
          </div>
        </div>
      </section>

      {/* Buttons */}
      <section className="neo-section mb-8">
        <h2 className="text-3xl font-black uppercase mb-6">Buttons</h2>
        
        <div className="space-y-6">
          <div>
            <h3 className="text-lg font-bold uppercase mb-4">Variants</h3>
            <div className="flex flex-wrap gap-4">
              <Button variant="default">Default</Button>
              <Button variant="secondary">Secondary</Button>
              <Button variant="destructive">Danger</Button>
              <Button variant="ghost">Ghost</Button>
              <Button variant="outline">Outline</Button>
            </div>
          </div>

          <div>
            <h3 className="text-lg font-bold uppercase mb-4">Sizes</h3>
            <div className="flex flex-wrap gap-4 items-center">
              <Button size="sm">Small</Button>
              <Button size="default">Default</Button>
              <Button size="lg">Large</Button>
            </div>
          </div>

          <div>
            <h3 className="text-lg font-bold uppercase mb-4">States</h3>
            <div className="flex flex-wrap gap-4">
              <Button>Normal</Button>
              <Button disabled>Disabled</Button>
              <Button className="animate-neo-pulse">Pulsing</Button>
            </div>
          </div>
        </div>
      </section>

      {/* Cards */}
      <section className="neo-section mb-8">
        <h2 className="text-3xl font-black uppercase mb-6">Cards</h2>
        
        <div className="grid md:grid-cols-3 gap-6">
          <div className="neo-card">
            <h3 className="text-xl font-black uppercase mb-2">Basic Card</h3>
            <p className="font-mono text-sm">
              Sharp corners, bold 2px border, hard shadow.
            </p>
          </div>
          
          <div className="neo-card-primary">
            <h3 className="text-xl font-black uppercase mb-2">Primary Card</h3>
            <p className="font-mono text-sm">
              Yellow background for important content.
            </p>
          </div>
          
          <div className="neo-card-secondary">
            <h3 className="text-xl font-black uppercase mb-2">Secondary Card</h3>
            <p className="font-mono text-sm">
              Cyan background for secondary info.
            </p>
          </div>

          <div className="neo-card-accent">
            <h3 className="text-xl font-black uppercase mb-2">Accent Card</h3>
            <p className="font-mono text-sm">
              Magenta background for highlights.
            </p>
          </div>
        </div>
      </section>

      {/* Input */}
      <section className="neo-section mb-8">
        <h2 className="text-3xl font-black uppercase mb-6">Input</h2>
        
        <div className="max-w-md space-y-4">
          <div>
            <label htmlFor="text-input" className="block font-black uppercase text-sm mb-2">
              Text Input
            </label>
            <input 
              id="text-input"
              type="text" 
              className="neo-input" 
              placeholder="Enter text..."
            />
          </div>
          
          <div>
            <label htmlFor="password-input" className="block font-black uppercase text-sm mb-2">
              Password
            </label>
            <input 
              id="password-input"
              type="password" 
              className="neo-input" 
              placeholder="••••••••"
            />
          </div>
        </div>
      </section>

      {/* Badges */}
      <section className="neo-section mb-8">
        <h2 className="text-3xl font-black uppercase mb-6">Badges</h2>
        
        <div className="flex flex-wrap gap-4">
          <span className="neo-badge-primary">Primary</span>
          <span className="neo-badge-secondary">Secondary</span>
          <span className="neo-badge-accent">Accent</span>
          <span className="neo-badge bg-danger text-white">Danger</span>
          <span className="neo-badge bg-success">Success</span>
          <span className="neo-badge bg-warning">Warning</span>
        </div>
      </section>

      {/* Shadows Demo */}
      <section className="neo-section mb-8">
        <h2 className="text-3xl font-black uppercase mb-6">Shadow Variants</h2>
        
        <div className="flex flex-wrap gap-8 items-end">
          <div className="text-center">
            <div className="w-20 h-20 bg-white border-2 border-black shadow-neo-sm mb-2"></div>
            <span className="font-mono text-xs">neo-sm</span>
          </div>
          <div className="text-center">
            <div className="w-20 h-20 bg-white border-2 border-black shadow-neo mb-2"></div>
            <span className="font-mono text-xs">neo</span>
          </div>
          <div className="text-center">
            <div className="w-20 h-20 bg-white border-2 border-black shadow-neo-lg mb-2"></div>
            <span className="font-mono text-xs">neo-lg</span>
          </div>
          <div className="text-center">
            <div className="w-20 h-20 bg-white border-2 border-black shadow-neo-xl mb-2"></div>
            <span className="font-mono text-xs">neo-xl</span>
          </div>
          <div className="text-center">
            <div className="w-20 h-20 bg-white border-2 border-black shadow-neo-inset mb-2"></div>
            <span className="font-mono text-xs">neo-inset</span>
          </div>
        </div>
      </section>
    </main>
  );
}