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

  it('keeps the axios message when the body has none', async () => {
    respond(500, null);
    await expect(apiClient.get('/api/v1/x')).rejects.toThrow('Request failed with status code 500');
  });

  it('rejects a 401 with no refresh token instead of retrying forever', async () => {
    let calls = 0;
    apiClient.defaults.adapter = async (config) => {
      calls++;
      const response = { status: 401, data: {}, headers: {}, config, statusText: '' } as AxiosResponse;
      throw new AxiosError('Unauthorized', undefined, config, null, response);
    };
    await expect(apiClient.get('/api/v1/x')).rejects.toThrow('No refresh token');
    expect(calls).toBe(1);
  });
});
