export default function DashboardPage() {
  return (
    <div className="max-w-6xl mx-auto">
      <h1 className="text-3xl font-black uppercase tracking-tight text-black mb-2">
        Dashboard
      </h1>
      <p className="text-gray-500 font-medium uppercase tracking-wider text-sm mb-8">
        Welcome to MaburVM
      </p>
      
      {/* Quick Stats */}
      <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-4 mb-8">
        <div className="bg-white border-4 border-black p-4 shadow-neo">
          <div className="flex items-center justify-between mb-2">
            <span className="text-xs font-black uppercase text-gray-500">Total VMs</span>
            <span className="w-3 h-3 bg-success"></span>
          </div>
          <p className="text-4xl font-black text-black">24</p>
          <p className="text-xs text-gray-500 mt-1">+3 this week</p>
        </div>
        
        <div className="bg-white border-4 border-black p-4 shadow-neo">
          <div className="flex items-center justify-between mb-2">
            <span className="text-xs font-black uppercase text-gray-500">Active Nodes</span>
            <span className="w-3 h-3 bg-primary"></span>
          </div>
          <p className="text-4xl font-black text-black">8</p>
          <p className="text-xs text-gray-500 mt-1">of 8 online</p>
        </div>
        
        <div className="bg-white border-4 border-black p-4 shadow-neo">
          <div className="flex items-center justify-between mb-2">
            <span className="text-xs font-black uppercase text-gray-500">Running</span>
            <span className="w-3 h-3 bg-secondary"></span>
          </div>
          <p className="text-4xl font-black text-black">18</p>
          <p className="text-xs text-gray-500 mt-1">75% utilization</p>
        </div>
        
        <div className="bg-white border-4 border-black p-4 shadow-neo">
          <div className="flex items-center justify-between mb-2">
            <span className="text-xs font-black uppercase text-gray-500">Alerts</span>
            <span className="w-3 h-3 bg-danger"></span>
          </div>
          <p className="text-4xl font-black text-black">3</p>
          <p className="text-xs text-gray-500 mt-1">requires attention</p>
        </div>
      </div>
      
      {/* Main Content Grid */}
      <div className="grid grid-cols-1 lg:grid-cols-3 gap-6">
        {/* Recent Activity */}
        <div className="lg:col-span-2 bg-white border-4 border-black p-6 shadow-neo">
          <h2 className="text-lg font-black uppercase tracking-tight text-black mb-4">
            Recent Activity
          </h2>
          <div className="space-y-4">
            {[
              { id: "1", time: "2 min ago", action: "VM 'web-server-01' started", user: "admin" },
              { id: "2", time: "15 min ago", action: "New template 'ubuntu-22.04' uploaded", user: "admin" },
              { id: "3", time: "1 hour ago", action: "Network 'vlan-10' configuration updated", user: "admin" },
              { id: "4", time: "2 hours ago", action: "Backup completed for 'db-server-01'", user: "system" },
              { id: "5", time: "3 hours ago", action: "Node 'node-04' came online", user: "system" },
            ].map((activity) => (
              <div key={activity.id} className="flex items-start gap-4 pb-4 border-b border-black last:border-0">
                <div className="w-2 h-2 bg-black mt-2 shrink-0"></div>
                <div className="flex-1">
                  <p className="text-sm font-bold">{activity.action}</p>
                  <p className="text-xs text-gray-500">
                    <span className="font-medium">{activity.user}</span> • {activity.time}
                  </p>
                </div>
              </div>
            ))}
          </div>
        </div>
        
        {/* Quick Actions */}
        <div className="bg-white border-4 border-black p-6 shadow-neo">
          <h2 className="text-lg font-black uppercase tracking-tight text-black mb-4">
            Quick Actions
          </h2>
          <div className="space-y-3">
            <button type="button" className="w-full p-3 text-left border-2 border-black hover:bg-gray-50 transition-colors shadow-neo-sm hover:shadow-neo active:translate-x-[2px] active:translate-y-[2px]">
              <span className="text-sm font-black uppercase">+ New VM</span>
            </button>
            <button type="button" className="w-full p-3 text-left border-2 border-black hover:bg-gray-50 transition-colors shadow-neo-sm hover:shadow-neo active:translate-x-[2px] active:translate-y-[2px]">
              <span className="text-sm font-black uppercase">Add Node</span>
            </button>
            <button type="button" className="w-full p-3 text-left border-2 border-black hover:bg-gray-50 transition-colors shadow-neo-sm hover:shadow-neo active:translate-x-[2px] active:translate-y-[2px]">
              <span className="text-sm font-black uppercase">Create Template</span>
            </button>
            <button type="button" className="w-full p-3 text-left border-2 border-black hover:bg-gray-50 transition-colors shadow-neo-sm hover:shadow-neo active:translate-x-[2px] active:translate-y-[2px]">
              <span className="text-sm font-black uppercase">View Network</span>
            </button>
          </div>
        </div>
      </div>
    </div>
  )
}