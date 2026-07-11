{*
  MaburVM client-area panel shown on the WHMCS service detail page.
  Day-to-day VM operation (start/stop/reboot/console) lives in the MaburVM
  client portal; this deep-links the customer there.
*}
<div class="row">
  <div class="col-sm-12">
    <div class="panel panel-default">
      <div class="panel-heading">
        <h3 class="panel-title">MaburVM</h3>
      </div>
      <div class="panel-body">
        {if $vmId}
          <p>Your virtual machine is provisioned.</p>
          <p><strong>VM ID:</strong> {$vmId}</p>
          <a href="{$panelUrl}/client/vms/{$vmId}" target="_blank" class="btn btn-primary">
            Manage VM
          </a>
          <a href="{$panelUrl}/client/vms/{$vmId}/console" target="_blank" class="btn btn-default">
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
