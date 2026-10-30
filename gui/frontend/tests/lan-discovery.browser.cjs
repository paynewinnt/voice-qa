const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const os = require('node:os');
const { pathToFileURL } = require('node:url');
const { test } = require('node:test');
const { chromium } = require('playwright');

// All discovery/connect APIs are mocked. No real LAN probing or ADB commands.
test('LAN discovery selects eligible devices, connects in bulk, cancels, and fits small screens', {timeout: 90000}, async () => {
    const browser = await chromium.launch({headless: true, ...(process.env.PLAYWRIGHT_CHANNEL ? {channel: process.env.PLAYWRIGHT_CHANNEL} : {})});
    const screenshots = process.env.LAN_SCREENSHOTS || fs.mkdtempSync(path.join(os.tmpdir(), 'voice-qa-lan-'));
    fs.mkdirSync(screenshots, {recursive: true});
    try {
        const page = await browser.newPage({viewport: {width: 1366, height: 768}});
        const errors = [];
        page.on('pageerror', error => errors.push(error.message));
        await page.route(/^https?:/, route => route.abort());
        await page.addInitScript(() => {
            window.lanMock = {events: new Map(), devices: ['192.168.50.8:5555'], scans: [], connections: [], cancels: [], holdScan: false, holdConnect: false};
            const mock = window.lanMock;
            const rows = [
                {ip:'192.168.50.1', mac:'12:34:56:78:9a:bc', name:'', address:'', state:'neighbor', sources:['邻居表'], canConnect:false},
                {ip:'192.168.50.2', mac:'12:34:56:78:9a:bd', name:'<img src=x onerror="window.badLAN=true">', address:'192.168.50.2:5555', port:5555, state:'port-open', sources:['邻居表','TCP'], canConnect:true},
                {ip:'192.168.50.3', name:'adb-wireless-display', address:'192.168.50.3:38123', port:38123, state:'advertised', sources:['mDNS'], canConnect:true},
                {ip:'192.168.50.4', name:'adb-pairing', address:'', port:37001, state:'pairing', sources:['mDNS'], canConnect:false},
                {ip:'192.168.50.8', name:'living-room-tv', address:'192.168.50.8:5555', port:5555, state:'port-open', sources:['TCP'], canConnect:true}
            ];
            window.runtime = {EventsOn(name, callback) { mock.events.set(name, callback); }};
            window.go = {main: {App: {
                GetConnectedDevices: async () => mock.devices,
                GetTextList: async () => [], GetConfig: async () => ({template: [{type:'voice',text:'$MAIN'}]}),
                GetVoices: async () => [{id:'piper:zh_CN-huayan-medium',name:'Piper',engine:'piper'}],
                GetPlayDir: async () => 'output', GetSubDirs: async () => [], SetPlayDevice: async () => {},
                GetAppVersion: async () => 'lan-test', GetDefaultUpdateURL: async () => '',
                CheckUpdate: async () => ({success:true,hasUpdate:false}),
                GetLANNetworks: async () => [{id:'wifi',name:'Wi-Fi',ip:'192.168.50.10',cidr:'192.168.50.0/24',scanCIDR:'192.168.50.0/24'}, {id:'ethernet',name:'Ethernet',ip:'10.20.0.2',cidr:'10.20.0.0/24',scanCIDR:'10.20.0.0/24'}],
                ScanLANDevices: async (requestID, options) => {
                    mock.scans.push(options);
                    mock.events.get('lan-scan-progress')({requestID:'stale-request',progress:{scanned:0,total:253,device:{ip:'192.168.50.200',canConnect:true}}});
                    for (const device of rows) mock.events.get('lan-scan-progress')({requestID,progress:{scanned:100,total:253,device}});
                    if (mock.holdScan) await new Promise(resolve => { mock.release = resolve; });
                    return {success:!mock.holdScan,canceled:mock.holdScan,message:mock.holdScan?'扫描已停止':'扫描完成：发现 5 台设备',devices:rows,scanned:mock.holdScan?100:253,total:253,warnings:[]};
                },
                ConnectLANDevices: async (requestID, addresses) => {
                    mock.connections.push([...addresses]);
                    if (mock.holdConnect) {
                        await new Promise(resolve => { mock.release = resolve; });
                        return {canceled:true,message:'连接已停止',items:[]};
                    }
                    const items = addresses.map(address => ({address,success:address.endsWith(':5555'),message:address.endsWith(':5555')?'已连接':'需要在设备上配对或授权'}));
                    for (const item of items) {
                        if (item.success) mock.devices.push(item.address);
                        mock.events.get('lan-connect-progress')({requestID,item});
                    }
                    return {message:'连接完成：1/2 台成功',items};
                },
                CancelLANOperation: async requestID => { mock.cancels.push(requestID); mock.release?.(); return true; }
            }}};
        });
        await page.goto(pathToFileURL(path.join(__dirname, '../dist/index.html')).href);
        await page.waitForFunction(() => document.querySelectorAll('.dev-check').length === 1);
        await page.locator('.dev-check').check();
        await page.locator('#devLANOpenBtn').click();
        await page.waitForFunction(() => devLAN.mode === '' && devLAN.devices.size === 5);
        assert.equal(await page.locator('#lanNetwork').inputValue(), 'wifi');
        assert.equal(await page.locator('#lanCIDR').inputValue(), '192.168.50.0/24');
        assert.equal(await page.locator('#lanDeviceRows tr').count(), 5);
        assert.equal(await page.locator('#lanDeviceRows img').count(), 0);
        assert.equal(await page.evaluate(() => !!window.badLAN), false);
        assert(await page.locator('#lanDeviceRows').getByLabel('选择 192.168.50.1', {exact:true}).isDisabled());
        assert(await page.getByLabel('选择 192.168.50.4', {exact:true}).isDisabled());
        assert(await page.getByLabel('选择 192.168.50.8', {exact:true}).isDisabled());
        await page.locator('#lanFilter').fill('50.3');
        await page.locator('#lanSelectAll').check();
        await page.locator('#lanFilter').fill('');
        assert.equal(await page.locator('#lanSelectedCount').textContent(), '已选 1 台');
        assert(await page.locator('#lanSelectAll').evaluate(el => el.indeterminate));
        await page.locator('#lanSelectAll').check();
        assert.equal(await page.locator('#lanSelectedCount').textContent(), '已选 2 台');

        await page.evaluate(() => { devLAN.devices.get('192.168.50.2').name = 'Android-TV'; devLANRender(); });
        for (const [width,height] of [[1920,1080],[1366,768],[1024,768],[390,844],[320,740]]) {
            await page.setViewportSize({width,height});
            const layout = await page.locator('.lan-dialog').evaluate(el => {
                const r=el.getBoundingClientRect();
                const table=el.querySelector('.lan-table-shell').getBoundingClientRect();
                const footer=el.querySelector('.lan-footer').getBoundingClientRect();
                return {left:r.left,right:r.right,top:r.top,bottom:r.bottom,tableHeight:table.height,tableBottom:table.bottom,footerTop:footer.top,pageWidth:document.documentElement.scrollWidth};
            });
            assert(layout.left >= 0 && layout.right <= width && layout.top >= 0 && layout.bottom <= height, `dialog escapes ${width}`);
            assert(layout.pageWidth <= width && layout.tableHeight >= 100 && layout.tableBottom <= layout.footerTop, `overflow at ${width}`);
            await page.screenshot({path:path.join(screenshots,`lan-${width}.png`)});
        }
        await page.setViewportSize({width:1366,height:768});
        await page.locator('#lanConnectBtn').click();
        await page.waitForFunction(() => devLAN.mode === '' && window.lanMock.connections.length === 1);
        assert.deepEqual(await page.evaluate(() => window.lanMock.connections[0]), ['192.168.50.2:5555','192.168.50.3:38123']);
        assert.equal(await page.locator('#lanStatus').textContent(), '连接完成：1/2 台成功');
        assert.equal(await page.locator('#lanSelectedCount').textContent(), '已选 1 台');
        assert.equal(await page.locator('.dev-check:checked').count(), 2);
        assert(await page.getByLabel('选择 192.168.50.2', {exact:true}).isDisabled());
        await page.screenshot({path:path.join(screenshots,'lan-connected.png')});

        await page.evaluate(() => { window.lanMock.holdConnect = true; });
        await page.locator('#lanConnectBtn').click();
        await page.waitForFunction(() => devLAN.mode === 'connect');
        await page.locator('#lanConnectBtn').click();
        await page.waitForFunction(() => devLAN.mode === '');
        assert.equal(await page.locator('#lanStatus').textContent(), '连接已停止');
        assert.equal(await page.evaluate(() => window.lanMock.cancels.length), 1);

        await page.evaluate(() => { window.lanMock.holdScan = true; });
        await page.locator('#lanScanBtn').click();
        await page.waitForFunction(() => devLAN.mode === 'scan');
        await page.keyboard.press('Escape');
        await page.waitForFunction(() => devLAN.mode === '' && !devLAN.visible);
        assert(!(await page.locator('#lanDialog').isVisible()));
        assert.equal(await page.evaluate(() => window.lanMock.cancels.length), 2);
        assert.deepEqual(errors, []);
        console.log(`LAN screenshots: ${screenshots}`);
    } finally { await browser.close(); }
});
