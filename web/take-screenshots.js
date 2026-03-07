const { chromium } = require('@playwright/test');

(async () => {
  const browser = await chromium.launch({ headless: true });
  const context = await browser.newContext({ viewport: { width: 1920, height: 1080 } });
  const page = await context.newPage();
  
  try {
    // Take screenshot of profile settings page
    await page.goto('http://localhost:3456/settings/profile', { waitUntil: 'networkidle' });
    await page.screenshot({ path: '.sisyphus/evidence/task-48-settings-2fa.png', fullPage: true });
    console.log('Profile settings screenshot saved');
    
    // Take screenshot of system settings page
    await page.goto('http://localhost:3456/settings/system', { waitUntil: 'networkidle' });
    await page.screenshot({ path: '.sisyphus/evidence/task-48-system-settings.png', fullPage: true });
    console.log('System settings screenshot saved');
    
  } catch (e) {
    console.error('Error:', e.message);
  } finally {
    await browser.close();
  }
})();