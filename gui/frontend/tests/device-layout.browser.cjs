const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { pathToFileURL } = require('node:url');
const { test } = require('node:test');
const { chromium } = require('playwright');

// Uses only a mock Wails bridge; no ADB commands or network requests are made.
test('device workspace keeps installation and command logs readable', { timeout: 90000 }, async () => {
    const browser = await chromium.launch({
        headless: true,
        ...(process.env.PLAYWRIGHT_CHANNEL ? { channel: process.env.PLAYWRIGHT_CHANNEL } : {})
    });
    const screenshots = process.env.LAYOUT_SCREENSHOTS || fs.mkdtempSync(path.join(os.tmpdir(), 'voice-qa-layout-'));
    fs.mkdirSync(screenshots, { recursive: true });
    try {
        const page = await browser.newPage();
        const errors = [];
        page.on('pageerror', error => errors.push(error.message));
        page.on('console', message => { if (message.type() === 'error') errors.push(message.text()); });
        await page.route(/^https?:/, route => route.abort());
        await page.addInitScript(() => {
            const devices = ['192.0.2.11:5555', '192.0.2.12:5555', '192.0.2.13:5555', '192.0.2.14:5555'];
            window.go = { main: { App: {
                GetConnectedDevices: async () => devices,
                GetTextList: async () => [],
                GetConfig: async () => ({ template: [{ type: 'voice', text: '$MAIN' }] }),
                GetVoices: async () => [{ id: 'piper:zh_CN-huayan-medium', name: 'Piper', engine: 'piper' }],
                GetPlayDir: async () => 'output',
                GetSubDirs: async () => [],
                SetPlayDevice: async () => {},
                GetAppVersion: async () => 'layout-test',
                GetDefaultUpdateURL: async () => '',
                CheckUpdate: async () => ({ success: true, hasUpdate: false }),
                StartManualScreenRecording: async () => ({ success: true, serial: devices[0], message: 'Recording started' }),
                StopManualScreenRecording: async () => ({ success: true, message: 'Recording saved' }),
                RunAdbCommand: async (serial, command) => ({ success: true, command, message: 'Command completed', output: 'versionName=2.4.34' })
            } } };
            window.runtime = { EventsOn() {} };
        });
        await page.setViewportSize({ width: 1920, height: 1009 });
        await page.goto(pathToFileURL(path.join(__dirname, '../dist/index.html')).href);
        await page.waitForFunction(() => document.querySelectorAll('#devListBody .dev-check').length === 4);
        assert.equal(await page.locator('.dev-device-column > section').count(), 3);
        assert.equal(await page.locator('#devOutput').getAttribute('tabindex'), '0');
        assert(await page.locator('#devOutput').isVisible());
        const emptyHeight = await page.locator('.dev-primary-workspace').evaluate(el => el.clientHeight);
        await page.evaluate(() => {
            document.getElementById('devAdbIp').value = '192.0.2.12';
            devSelectedApks = ['D:/APK/测试应用_V2.4.34_prod_release_202610261540021_signed.apk'];
            devRenderApkList();
            document.getElementById('devInstallStatus').textContent = '192.0.2.12:5555: 正在直接流式安装：已耗时 2m25s，等待设备安装结果 (1/4)';
            devFillCommand('shell dumpsys package com.example.app');
            for (let index = 0; index < 100; index++) {
                devAppendOutput(`192.0.2.12:5555: ${index % 2 ? '正在直接流式安装：已耗时 2m25s，等待设备安装结果' : '正在直接流式覆盖安装（adb install --streaming --no-incremental -r，最多 30 分钟）；不在设备端另存 APK'}`, 'info');
                devAppendCommandOutput(`Package com.example.app: versionCode=2434 versionName=2.4.34 sample=${index}`, 'normal');
            }
        });
        assert.equal(await page.locator('.dev-primary-workspace').evaluate(el => el.clientHeight), emptyHeight);

        for (const [width, height] of [[1920, 1009], [1366, 768], [1280, 720], [1024, 768], [390, 844], [320, 740]]) {
            await page.setViewportSize({ width, height });
            await page.evaluate(() => window.scrollTo(0, 0));
            const layout = await page.evaluate(() => {
                const rect = selector => {
                    const { x, y, width, height, bottom, right } = document.querySelector(selector).getBoundingClientRect();
                    return { x, y, width, height, bottom, right };
                };
                return {
                    viewport: innerWidth, pageWidth: document.documentElement.scrollWidth,
                    workspace: rect('.dev-primary-workspace'), left: rect('.dev-device-column'),
                    install: rect('.dev-install-section'), command: rect('.dev-command-section'),
                    logcat: rect('.dev-log-section'), installLog: rect('#devOutput'), commandLog: rect('#devCommandOutput')
                };
            });
            assert(layout.pageWidth <= width, `page overflow at ${width}: ${layout.pageWidth}`);
            assert(layout.installLog.height >= 240, `APK log too short at ${width}: ${layout.installLog.height}`);
            assert(layout.commandLog.height >= 160, `command log too short at ${width}`);
            assert(layout.installLog.bottom < layout.install.bottom, `APK log escapes panel at ${width}`);
            assert(layout.commandLog.bottom < layout.command.bottom, `command log escapes panel at ${width}`);
            assert(layout.logcat.y >= layout.workspace.bottom, `logcat overlaps workspace at ${width}`);
            if (width > 1080) {
                assert(Math.abs(layout.install.y - layout.left.y) < 1);
                assert(Math.abs(layout.command.y - layout.left.y) < 1);
                assert(Math.abs(layout.install.bottom - layout.left.bottom) < 1);
                assert(Math.abs(layout.command.bottom - layout.left.bottom) < 1);
                assert(Math.abs(layout.command.width - layout.install.width * 2) < 2);
                assert(layout.install.x >= layout.left.right && layout.command.x >= layout.install.right);
            } else if (width > 760) {
                assert(Math.abs(layout.install.y - layout.left.y) < 1);
                assert(layout.command.y >= layout.install.bottom);
            } else {
                assert(layout.install.y >= layout.left.bottom);
                assert(layout.command.y >= layout.install.bottom);
            }
            for (const selector of ['#devOutput', '#devCommandOutput']) {
                assert(await page.locator(selector).evaluate(el => el.scrollHeight > el.clientHeight));
                await page.locator(selector).evaluate(el => { el.scrollTop = el.scrollHeight; });
            }
            await page.screenshot({ path: path.join(screenshots, `device-${width}.png`), fullPage: true });
            console.log(`${width}x${height}: APK log ${Math.round(layout.installLog.height)}px, command log ${Math.round(layout.commandLog.height)}px`);
        }

        await page.setViewportSize({ width: 1366, height: 768 });
        await page.evaluate(() => {
            globalDevices = Array.from({ length: 30 }, (_, index) => `192.0.2.${index + 1}:5555`);
            devRenderList();
            devSelectedApks = Array.from({ length: 20 }, (_, index) => `D:/APK/Long_application_name_with_build_version_${index}_signed.apk`);
            devRenderApkList();
            devCommandHistory = Array.from({ length: 10 }, (_, index) => `shell getprop test.property.${index}`);
            devRenderCommandHistory();
        });
        await page.locator('#devCommandHistoryBtn').click();
        assert((await page.locator('#devCommandOutput').boundingBox()).height >= 160);
        const inputsBox = await page.locator('.dev-command-inputs').boundingBox();
        const outputBox = await page.locator('#devCommandOutput').boundingBox();
        // The expanded history is clipped by the scrollable controls, not painted over the log.
        assert(inputsBox.y + inputsBox.height <= outputBox.y);
        await page.locator('.dev-command-history-item').last().click();
        assert.equal(await page.locator('#devCommandInput').inputValue(), 'shell getprop test.property.9');
        await page.locator('#devRunCommandBtn').click();
        await page.waitForFunction(() => document.getElementById('devCommandStatus').textContent === 'Command completed');
        await page.locator('#devRecordDevice').selectOption('192.0.2.11:5555');
        await page.locator('#devRecordToggleBtn').click();
        await page.waitForFunction(() => document.getElementById('devRecordToggleBtn').textContent === '停止录屏并保存');
        await page.locator('#devRecordToggleBtn').click();
        await page.waitForFunction(() => document.getElementById('devRecordToggleBtn').textContent === '开始录屏');
        assert(await page.locator('#devApkList').evaluate(el => el.scrollHeight > el.clientHeight));
        assert(await page.locator('.dev-device-section .table-shell').evaluate(el => el.scrollHeight > el.clientHeight));
        assert((await page.locator('#devOutput').boundingBox()).height >= 240);
        await page.evaluate(() => window.scrollTo(0, 0));
        await page.screenshot({ path: path.join(screenshots, 'device-many-items.png'), fullPage: true });
        assert.deepEqual(errors, []);
        console.log(`Screenshots: ${screenshots}`);
    } finally {
        await browser.close();
    }
});
