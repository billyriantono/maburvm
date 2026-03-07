const puppeteer = require('puppeteer-core');
const path = require('path');

(async () => {
  const browser = await puppeteer.launch({
    executablePath: '/opt/google/chrome/chrome',
    args: [
      '--no-sandbox',
      '--disable-setuid-sandbox',
      '--disable-dev-shm-usage',
      '--disable-accelerated-2d-canvas',
      '--no-first-run',
      '--no-zygote',
      '--single-process',
      '--disable-gpu'
    ],
    headless: true,
    defaultViewport: { width: 1400, height: 2000 }
  });

  const page = await browser.newPage();
  
  // Navigate to the audit-logs page
  await page.goto('http://localhost:3000/audit-logs', { waitUntil: 'networkidle0' });
  
  // Wait a moment for any animations
  await new Promise(resolve => setTimeout(resolve, 2000));
  
  // Take screenshot - absolute path
  const screenshotPath = '/root/Works/MaburVM/.sisyphus/evidence/task-46-audit-logs.png';
  await page.screenshot({ 
    path: screenshotPath,
    fullPage: true 
  });
  
  console.log(`Screenshot saved to: ${screenshotPath}`);
  
  await browser.close();
})();