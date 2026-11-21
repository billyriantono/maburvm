import { AxiosError, type AxiosResponse, type InternalAxiosRequestConfig } from 'axios';
import { afterEach, describe, expect, it } from 'vitest';
import { apiClient } from './api-client';

const defaultAdapter = apiClient.defaults.adapter;
afterEach(() => {
  apiClient.defaults.adapter = defaultAdapter;
});

// respond makes every request fail with the given status and body.
function respond(status: number, data: unknown) {
  apiClient.defaults.adapter = async (config: InternalAxiosRequestConfig) => {
    const response = { status, data, headers: {}, config, statusText: '' } as AxiosResponse;
    throw new AxiosError(`Request failed with status code ${status}`, undefined, config, null, response);
  };
}

describe('apiClient error handling', () => {
  it('surfaces the server message instead of the axios default', async () => {
    respond(400, { success: false, message: 'Domain is already active' });
    await expect(apiClient.get('/api/v1/x')).rejects.toThrow('Domain is already active');
  });

  it('falls back to the error field', async () => {
    respond(409, { error: 'that region has no capacity' });
    await expect(apiClient.get('/api/v1/x')).rejects.toThrow('that region has no capacity');
  });

  it('reads the error envelope, and never shows its object', async () => {
    respond(409, { success: false, error: { code: 'conflict', message: 'A job is already running' } });
    await expect(apiClient.post('/api/v1/x')).rejects.toThrow('A job is already running');
    respond(503, { success: false, error: { code: 'maintenance', message: 'Back at noon.' } });
    await expect(apiClient.post('/api/v1/x')).rejects.toThrow('Back at noon.');
  });

  it('says why a change waits in maintenance mode, and treats other 503s as the panel being down', async () => {
    respond(503, { code: 'maintenance', message: 'The panel is being upgraded.' });
    await expect(apiClient.post('/api/v1/vms')).rejects.toThrow('The panel is being upgraded.');
    respond(503, { message: 'upstream gone' });
    await expect(apiClient.get('/api/v1/x')).rejects.toThrow('The panel did not answer');
  });

  it('keeps the axios message when the body has none', async () => {
    respond(500, null);
    await expect(apiClient.get('/api/v1/x')).rejects.toThrow('Request failed with status code 500');
  });

  it('rejects a 401 once, without trying to renew the session', async () => {
    let calls = 0;
    apiClient.defaults.adapter = async (config) => {
      calls++;
      const response = { status: 401, data: { message: 'Session has been revoked' }, headers: {}, config, statusText: '' } as AxiosResponse;
      throw new AxiosError('Unauthorized', undefined, config, null, response);
    };
    await expect(apiClient.get('/api/v1/x')).rejects.toThrow('Session has been revoked');
    expect(calls).toBe(1);
  });
});

describe('apiClient session handling in the browser', () => {
  const page = (pathname: string, search = '') => {
    const location = { pathname, search, href: pathname + search };
    const doc = { cookie: 'accessToken=abc' };
    Object.assign(globalThis, { window: { location }, document: doc });
    return { location, doc };
  };
  afterEach(() => {
    delete (globalThis as { window?: unknown }).window;
    delete (globalThis as { document?: unknown }).document;
  });

  it('sends an ended session to sign-in and back to the same page', async () => {
    const { location, doc } = page('/vms/42', '?tab=network');
    respond(401, { message: 'Session has been revoked' });
    await expect(apiClient.get('/api/v1/vms/42')).rejects.toThrow();
    expect(location.href).toBe('/login?next=%2Fvms%2F42%3Ftab%3Dnetwork');
    // The panel's 401 clears the HttpOnly cookie; the page never touches it.
    expect(doc.cookie).toBe('accessToken=abc');
  });

  it('stays on the sign-in pages', async () => {
    const { location } = page('/reset-password', '?token=x');
    respond(401, {});
    await expect(apiClient.get('/api/v1/auth/me')).rejects.toThrow();
    expect(location.href).toBe('/reset-password?token=x');
  });

  it('sends an administrator who must set up two-factor to the profile page', async () => {
    const { location } = page('/vms');
    respond(403, { code: 'two_factor_required', message: 'Set up two-factor authentication to continue' });
    await expect(apiClient.get('/api/v1/vms')).rejects.toThrow('Set up two-factor authentication to continue');
    expect(location.href).toBe('/settings/profile');

    const onProfile = page('/settings/profile');
    await expect(apiClient.get('/api/v1/dashboard/stats')).rejects.toThrow();
    expect(onProfile.location.href).toBe('/settings/profile');
  });

  it('leaves any other 403 to the page', async () => {
    const { location } = page('/users/7');
    respond(403, { error: 'only_the_founding_administrator_may_change_admin_accounts', message: 'Only the founding administrator can change another administrator\'s account.' });
    await expect(apiClient.put('/api/v1/users/7', {})).rejects.toThrow('Only the founding administrator');
    expect(location.href).toBe('/users/7');
  });
});
