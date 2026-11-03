const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const { test } = require('node:test');

const html = fs.readFileSync(path.join(__dirname, '../dist/index.html'), 'utf8');
const scripts = [...html.matchAll(/<script\b[^>]*>([\s\S]*?)<\/script>/gi)].map(match => match[1]);

function loadApp() {
    const elements = new Map();
    function element(id) {
        if (!elements.has(id)) elements.set(id, {
            value: '', checked: false, disabled: false, textContent: '', innerHTML: '', style: {},
            classList: { add() {}, remove() {}, toggle() {}, contains() { return false; } },
            appendChild() {}, append() {}, replaceChildren() {}, addEventListener() {}, setAttribute() {}
        });
        return elements.get(id);
    }
    const events = new Map();
    const window = { go: { main: { App: {} } }, runtime: { EventsOn(name, handler) { events.set(name, handler); } } };
    const context = vm.createContext({
        window, console, setTimeout, clearTimeout, localStorage: { getItem() { return null; } },
        document: { getElementById: element, querySelectorAll() { return []; }, addEventListener() {}, createElement: () => element(Symbol()) }
    });
    scripts.forEach(script => new vm.Script(script).runInContext(context));
    return { context, element, events, window, run: script => vm.runInContext(script, context) };
}

test('manual metrics exclude automatic and failed captures', () => {
    const app = loadApp();
    for (const reason of ['batch', 'timeout', '']) {
        assert.equal(app.run(`perfRecordFromRun({success:true, stopReason:${JSON.stringify(reason)}, manualDurationMs:30000}).duration`), 0);
    }
    assert.equal(app.run('perfRecordFromRun({success:false, stopReason:"manual", manualDurationMs:30000}).duration'), 0);
    assert.equal(app.run('perfRecordFromRun({success:true, stopReason:"manual", manualDurationMs:15000}).duration'), 15000);
});

test('record selection and averages stay within launch groups', () => {
    const app = loadApp();
    app.element('perfStatsTotal').checked = true;
    app.element('perfStatsManual').checked = true;
    app.run(`perfRecords = [
        {serial:'one', resultDir:'/one', success:true, stopReason:'manual', totalTimeMs:1000, manualDurationMs:10000},
        {serial:'two', resultDir:'/two', success:true, stopReason:'manual', totalTimeMs:9000, manualDurationMs:90000}
    ].map(perfRecordFromRun); perfCalcStats();`);
    const stats = app.element('perfStatsText').innerHTML;
    assert.match(stats, /分为 <b>2<\/b> 组/);
    assert.match(stats, /1000\.00/);
    assert.match(stats, /9000\.00/);
    assert.doesNotMatch(stats, /5000\.00/);
    app.run('perfSelectRecord(0, false)');
    assert.deepEqual(JSON.parse(app.run('JSON.stringify(perfReportResultDirs())')), ['/two']);
    assert.equal(app.element('perfSelectAllRecords').indeterminate, true);
    app.run('perfSelectAllRecords(false)');
    assert.equal(app.element('perfAggregateReportBtn').disabled, true);
    const canonical = app.run(`perfRecordGroupKey({packageName:'com.example', component:'com.example/com.example.Main'})`);
    for (const component of ['com.example/.Main', 'com.example/Main']) {
        assert.equal(app.run(`perfRecordGroupKey({packageName:'com.example', component:${JSON.stringify(component)}})`), canonical);
    }
});

test('sparse samples cannot pass in the GUI', () => {
    const app = loadApp();
    app.run(`testDevice = {
        initialPid:1, finalPid:1,
        samples: {sampleCount:4, topCpuPercent:{count:4, mean:0, max:0}, pssKb:{count:4, mean:10240}, processAliveSamples:4, foregroundSamples:4},
        gfxinfo: {available:true, jankyPercent:0, p90Ms:16}
    }; testThresholds = {cpuAveragePercent:50, cpuPeakPercent:120, pssAverageMb:120, jankyFramesPercent:20, frameP90Ms:24};`);
    assert.equal(app.run('perfOverallDevicePassed(testDevice, testThresholds)'), true);
    assert.equal(app.run('testDevice.samples.topCpuPercent.count = 1; perfOverallDevicePassed(testDevice, testThresholds)'), false);
    assert.equal(app.run('testDevice.samples.topCpuPercent.count = 4; testDevice.gfxinfo.available = false; perfOverallDevicePassed(testDevice, testThresholds)'), false);
    assert.equal(app.run('testDevice.gfxinfo.available = true; testDevice.rounds = 4; testDevice.scenariosPerRound = 12; perfOverallDevicePassed(testDevice, testThresholds)'), false);
});

test('verification disables startup actions and delayed timeout cannot stop a new session', () => {
    const app = loadApp();
    app.run('perfVerifyRunning = true; perfSyncTimerControl();');
    assert.equal(app.element('perfTimerToggleBtn').disabled, true);
    assert.equal(app.element('perfBatchBtn').disabled, true);
    app.run(`initEventListeners(); perfVerifyRunning = false; perfTimerRunning = true;
        perfTimerAction = 'start'; perfCurrentLaunch = null; perfRecords = [{resultDir:'/old'}];`);
    app.events.get('perfetto-timeout')({resultDir: '/old'});
    assert.equal(app.run('perfTimerRunning'), true);
});

test('manual stop displays the backend duration, including timeout race results', async () => {
    for (const reason of ['manual', 'timeout']) {
        const app = loadApp();
        app.window.go.main.App.PerfStopLaunchTrace = async () => ({success:true, reason, resultDir:'/current', run:{success:true, stopReason:reason, manualDurationMs:reason === 'manual' ? 12345 : 0, resultDir:'/current'}});
        app.run('perfTimerRunning = true; perfCurrentLaunch = {resultDir:"/current"};');
        await app.run('perfStopTimer()');
        assert.equal(app.run('perfRecords.length'), 1);
        assert.equal(app.run('perfRecords[0].duration'), reason === 'manual' ? 12345 : 0);
        assert.equal(app.run('perfTimerRunning'), false);
    }
});

test('APK install freezes targets and files, blocks duplicate requests, and shows stages', async () => {
    const app = loadApp();
    const calls = [];
    let finishFirst;
    app.window.go.main.App.InstallApk = async (serial, apk) => {
        calls.push([serial, apk]);
        if (calls.length === 1) await new Promise(resolve => { finishFirst = resolve; });
        return {success: true, message: '安装成功（保留数据覆盖安装）'};
    };
    app.run(`globalDevices = ['device-a']; devSelectedApks = ['one.apk','two.apk']; initEventListeners();`);
    const installing = app.run('devInstallApks()');
    await app.run('devInstallApks()');
    app.run(`devRemoveApk(0); globalDevices = ['device-b']; devSyncInstallTargetState();`);
    assert.equal(app.run('devSelectedApks.length'), 2);
    assert.equal(app.element('devInstallBtn').disabled, true);
    assert.equal(app.element('devSelectApksBtn').disabled, true);
    app.events.get('apk-install-progress')({serial:'device-a', apkFile:'one.apk', message:'正在直接流式覆盖安装'});
    assert.match(app.element('devInstallStatus').textContent, /正在直接流式覆盖安装/);
    app.events.get('apk-install-progress')({serial:'other', apkFile:'one.apk', message:'wrong target'});
    assert.doesNotMatch(app.element('devInstallStatus').textContent, /wrong target/);
    const progressLines = [];
    app.element('devOutput').appendChild = line => progressLines.push(line.textContent);
    const transferProgress = '正在直接流式安装：已耗时 2m25s，等待设备安装结果';
    app.events.get('apk-install-progress')({serial:'device-a', apkFile:'one.apk', message:transferProgress});
    assert(app.element('devInstallStatus').textContent.includes(transferProgress));
    assert.equal(progressLines.length, 1);
    assert.doesNotMatch(progressLines[0], /one\.apk/);
    app.run(`devSelectedApks = ['new.apk'];`);
    finishFirst();
    await installing;
    assert.deepEqual(calls, [['device-a','one.apk'],['device-a','two.apk']]);
    assert.equal(app.run('devInstallBusy'), false);
    assert.equal(app.element('devSelectApksBtn').disabled, false);
    assert.equal(app.element('devInstallStatus').textContent, '安装完成：2/2 成功');
});

test('APK install errors restore controls', async () => {
    const app = loadApp();
    app.window.go.main.App.InstallApk = async () => { throw Error('offline'); };
    app.run(`globalDevices = ['device-a']; devSelectedApks = ['one.apk'];`);
    await app.run('devInstallApks()');
    assert.equal(app.run('devInstallBusy'), false);
    assert.equal(app.element('devInstallBtn').disabled, false);
    assert.equal(app.element('devInstallStatus').textContent, '安装完成：0/1 成功');
});

test('screenshot locks duplicate clicks and displays its saved path without changing recording status', async () => {
    const app = loadApp();
    const calls = [];
    let complete;
    app.window.go.main.App.CaptureDeviceScreenshot = serial => {
        calls.push(serial);
        return new Promise(resolve => { complete = resolve; });
    };
    app.element('devRecordDevice').value = 'device-a';
    app.element('devRecordStatus').textContent = '录屏已保存';
    const capture = app.run('devTakeScreenshot()');
    await app.run('devTakeScreenshot()');
    assert.deepEqual(calls, ['device-a']);
    assert.equal(app.element('devScreenshotBtn').disabled, true);
    assert.equal(app.element('devRecordDevice').disabled, true);
    app.run('globalDevices = ["device-b"]; devSyncRecordDeviceSelect();');
    assert.equal(app.element('devRecordDevice').value, 'device-a');
    complete({ success: true, clipboardCopied: true, serial: 'device-a', path: 'D:/中文输出/screenshot-1.png', message: '截图已保存并复制：D:/中文输出/screenshot-1.png' });
    await capture;
    assert.equal(app.element('devScreenshotStatus').textContent, '截图已保存并复制：D:/中文输出/screenshot-1.png');
    assert.equal(app.element('devRecordStatus').textContent, '录屏已保存');
    assert.equal(app.element('devScreenshotBtn').disabled, false);
    assert.equal(app.element('devRecordDevice').disabled, false);
    assert.equal(app.run('devScreenshotBusy'), false);
});

test('screenshot remains available during recording and preserves the recording target', async () => {
    const app = loadApp();
    const calls = [];
    app.window.go.main.App.CaptureDeviceScreenshot = async serial => {
        calls.push(serial);
        return { success: true, clipboardCopied: true, path: 'output/screenshot.png', message: '截图已保存并复制：output/screenshot.png' };
    };
    app.run('devRecording = true; devRecordingSerial = "recording-device";');
    app.element('devRecordDevice').value = 'other-device';
    app.element('devRecordStatus').textContent = '录屏中';
    await app.run('devTakeScreenshot()');
    assert.deepEqual(calls, ['recording-device']);
    assert.equal(app.run('devRecording'), true);
    assert.equal(app.element('devRecordToggleBtn').textContent, '停止录屏并保存');
    assert.equal(app.element('devRecordStatus').textContent, '录屏中');
    assert.equal(app.element('devRecordDevice').disabled, true);
    assert.equal(app.element('devScreenshotBtn').disabled, false);
    app.run('devRecordingBusy = true; devRenderRecordingControl();');
    await app.run('devTakeScreenshot()');
    assert.equal(calls.length, 1);
    assert.equal(app.element('devScreenshotBtn').disabled, true);
});

test('screenshot errors restore controls and show the original error safely', async () => {
    for (const throws of [false, true]) {
        const app = loadApp();
        app.window.go.main.App.CaptureDeviceScreenshot = async () => {
            if (throws) throw Error('device offline');
            return { success: false, message: '<device> offline' };
        };
        await app.run('devTakeScreenshot()');
        assert.match(app.element('devScreenshotStatus').textContent, /截图失败：.*offline/);
        assert.equal(app.element('devScreenshotStatus').innerHTML, '');
        assert.equal(app.element('devScreenshotBtn').disabled, false);
        assert.equal(app.run('devScreenshotBusy'), false);
    }
});

test('clipboard failure is distinct from screenshot failure and keeps the saved path', async () => {
    const app = loadApp();
    app.window.go.main.App.CaptureDeviceScreenshot = async () => ({
        success: true, clipboardCopied: false, path: 'output/screenshot.png',
        message: '截图已保存，复制失败：clipboard busy；output/screenshot.png'
    });
    await app.run('devTakeScreenshot()');
    assert.match(app.element('devScreenshotStatus').textContent, /截图已保存，复制失败/);
    assert.match(app.element('devScreenshotStatus').textContent, /output\/screenshot\.png/);
    assert.doesNotMatch(app.element('devScreenshotStatus').textContent, /截图失败/);
    assert.equal(app.element('devScreenshotBtn').disabled, false);
});
