{*
  MaburVM client-area panel shown on the WHMCS service detail page: the VM's
  state and address, read live from the panel. Day-to-day operation
  (start/stop/reboot/console) lives in the MaburVM client portal; this
  deep-links the customer there.
*}
<div class="row">
  <div class="col-sm-12">
    <div class="panel panel-default">
      <div class="panel-heading">
        <h3 class="panel-title">MaburVM</h3>
      </div>
      <div class="panel-body">
        {if $vmId}
          {if $vmStateLabel}
            <p><strong>Status:</strong> {$vmStateLabel|escape}</p>
          {else}
            <p>The server's current status could not be read. The links below still work.</p>
          {/if}
          {if $vmIp}<p><strong>IP address:</strong> {$vmIp|escape}</p>{/if}
          {if $vmUsername}<p><strong>Username:</strong> {$vmUsername|escape}</p>{/if}
          <p><strong>VM ID:</strong> {$vmId|escape}</p>
          {if $vmState eq 'failed'}
            <p>The installation did not finish. Please contact support.</p>
          {elseif $vmState eq 'error'}
            <p>Something went wrong with this server. Please contact support.</p>
          {/if}
          <a href="{$panelUrl}/client/vms/{$vmId|escape:'url'}" target="_blank" class="btn btn-primary">
            Manage VM
          </a>
          <a href="{$panelUrl}/client/vms/{$vmId|escape:'url'}/console" target="_blank" class="btn btn-default">
            Open Console
          </a>
        {else}
          <p>Your virtual machine is being provisioned. Please check back shortly.</p>
          <a href="{$panelUrl}/client/dashboard" target="_blank" class="btn btn-primary">
            Open Client Portal
          </a>
        {/if}
      </div>
    </div>
  </div>
</div>
