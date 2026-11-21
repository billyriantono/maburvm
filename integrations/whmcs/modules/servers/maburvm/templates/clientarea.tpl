{*
  MaburVM client-area panel shown on the WHMCS service detail page: the VM's
  state and address, read live from the panel. Day-to-day operation
  (start/stop/reboot/console) lives in the MaburVM client portal. "Open
  control panel" signs the customer in there with one click (the module's
  ServiceSingleSignOn), so no second password is needed.
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
          <a href="clientarea.php?action=productdetails&amp;id={$serviceId|escape:'url'}&amp;dosinglesignon=1" target="_blank" class="btn btn-primary">
            Open control panel
          </a>
          <a href="{$panelUrl}/client/vms/{$vmId|escape:'url'}/console" target="_blank" class="btn btn-default">
            Open console
          </a>
        {else}
          <p>Your virtual machine is being provisioned. Please check back shortly.</p>
          <a href="clientarea.php?action=productdetails&amp;id={$serviceId|escape:'url'}&amp;dosinglesignon=1" target="_blank" class="btn btn-primary">
            Open control panel
          </a>
        {/if}
      </div>
    </div>
  </div>
</div>
