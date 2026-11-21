{*
  MaburVM client-area panel shown on the WHMCS service detail page: the VM's
  state and address, read live from the panel, power buttons, the console, a
  new root password, a reinstall form and the last day's graphs. Every action
  posts back to this page (modop=custom), runs as this service's module
  function, and the panel checks it names this service's VM. "Open control
  panel" signs the customer in to the MaburVM portal with one click.
*}
{assign var=action value="clientarea.php?action=productdetails&id={$serviceId|escape:'url'}&modop=custom&a="}
<div class="row">
  <div class="col-sm-12">
    <div class="panel panel-default">
      <div class="panel-heading">
        <h3 class="panel-title">Server</h3>
      </div>
      <div class="panel-body">
        {if $vmId}
          {if $vmStateLabel}
            <p><strong>Status:</strong> {$vmStateLabel|escape}{if $vmPower && $vmState eq 'ready'} ({$vmPower|escape}){/if}</p>
          {else}
            <p>The server's current status could not be read. The links below still work.</p>
          {/if}
          {if $vmIp}<p><strong>IP address:</strong> {$vmIp|escape}</p>{/if}
          {if $vmUsername}<p><strong>Username:</strong> {$vmUsername|escape}</p>{/if}
          {if $vmState eq 'failed'}
            <p>The installation did not finish. Please contact support.</p>
          {elseif $vmState eq 'error'}
            <p>Something went wrong with this server. Please contact support.</p>
          {/if}

          {if $vmState eq 'ready' && $serviceActive}
            <div class="btn-toolbar" role="group" aria-label="Power">
              {if $vmPower eq 'running'}
                <form method="post" action="{$action}clientRestart" class="d-inline-block">
                  <input type="hidden" name="token" value="{$token}" />
                  <button type="submit" class="btn btn-default">Restart</button>
                </form>
                <form method="post" action="{$action}clientStop" class="d-inline-block">
                  <input type="hidden" name="token" value="{$token}" />
                  <button type="submit" class="btn btn-default">Shut down</button>
                </form>
              {else}
                <form method="post" action="{$action}clientStart" class="d-inline-block">
                  <input type="hidden" name="token" value="{$token}" />
                  <button type="submit" class="btn btn-success">Start</button>
                </form>
              {/if}
              <form method="post" action="{$action}clientConsole" target="_blank" class="d-inline-block">
                <input type="hidden" name="token" value="{$token}" />
                <button type="submit" class="btn btn-default">Open console</button>
              </form>
              <a href="clientarea.php?action=productdetails&amp;id={$serviceId|escape:'url'}&amp;dosinglesignon=1" target="_blank" class="btn btn-primary">
                Open control panel
              </a>
            </div>

            {if $graphs}
              <h4>Last 24 hours</h4>
              <div class="row">
                {foreach $graphs as $g}
                  <div class="col-sm-6">
                    <p><strong>{$g.label|escape}</strong> <span class="text-muted">now {$g.now|escape}</span></p>
                    <svg viewBox="0 0 300 60" width="100%" height="60" preserveAspectRatio="none" role="img" aria-label="{$g.label|escape}, now {$g.now|escape}">
                      <polyline points="{$g.points|escape}" fill="none" stroke="currentColor" stroke-width="1.5" vector-effect="non-scaling-stroke" />
                    </svg>
                  </div>
                {/foreach}
              </div>
            {/if}

            <h4>Root password</h4>
            {if $vmPower eq 'running'}
              <form method="post" action="{$action}clientResetPassword">
                <input type="hidden" name="token" value="{$token}" />
                <p>Sets a new root password on the running server. The new password is shown with this service's login details.</p>
                <button type="submit" class="btn btn-default">Set a new root password</button>
              </form>
            {else}
              <p>Start the server to set a new root password.</p>
            {/if}

            <h4>Reinstall</h4>
            {if $vmPower neq 'stopped'}
              <p>Shut the server down to reinstall it.</p>
            {elseif $templates}
              <form method="post" action="{$action}clientReinstall">
                <input type="hidden" name="token" value="{$token}" />
                <div class="form-group">
                  <label for="maburvm-template">Operating system</label>
                  <select id="maburvm-template" name="template_id" class="form-control">
                    {foreach $templates as $t}
                      <option value="{$t.id|escape}">{$t.name|escape}</option>
                    {/foreach}
                  </select>
                </div>
                <div class="checkbox">
                  <label><input type="checkbox" name="confirm" value="1" required /> Erase everything on this server and install the system above. Its addresses stay.</label>
                </div>
                <button type="submit" class="btn btn-danger">Reinstall</button>
              </form>
            {else}
              <p>The list of operating systems could not be read. Try again later, or reinstall from the control panel.</p>
            {/if}
          {else}
            <a href="clientarea.php?action=productdetails&amp;id={$serviceId|escape:'url'}&amp;dosinglesignon=1" target="_blank" class="btn btn-primary">
              Open control panel
            </a>
          {/if}
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
