/* ============================================================
   FreeV6 万圣节彩蛋（随 v0.5.2 发布）
   纯前端、零依赖、自包含：本地时间 10/31（万圣节）当天自动生效，
   其余日期完全无感、零开销、无需发版即可自然消失。

   生效时做三件事：
     1. 仪表盘漂浮南瓜 / 蝙蝠 / 幽灵氛围装饰（不拦截点击）
     2. 左上角品牌标「F 6」临时变成小南瓜 🎃，点击触发糖果雨
     3. 当天首次启动弹出「万圣节快乐」轻提醒

   调试：浏览器打开 web/index.html 时加 ?halloween=1 可强制开启；
         或在控制台执行 localStorage.setItem('freev6-halloween-force','1') 后刷新。
   ============================================================ */
(function () {
  'use strict';

  var FORCE_KEY = 'freev6-halloween-force';
  var GREET_KEY = 'freev6-halloween-greeted';

  // 是否为万圣节（本地时间 10 月 31 日；Date.getMonth() 从 0 起，9 = 10 月）
  function isHalloween(now) {
    var d = now || new Date();
    return d.getMonth() === 9 && d.getDate() === 31;
  }

  // 是否被调试开关强制开启（URL 参数或 localStorage）
  function isForced() {
    try {
      if (/(\?|&)halloween=1\b/.test(window.location.search)) return true;
      if (window.location.hash.indexOf('halloween') !== -1) return true;
      return window.localStorage.getItem(FORCE_KEY) === '1';
    } catch (e) {
      return false;
    }
  }

  // 非万圣节且未强制 → 什么都不做，立即退出
  if (!isHalloween() && !isForced()) return;

  // 今日键（用于「每天只问候一次」），本地时间
  var todayKey = '';
  try {
    var n = new Date();
    todayKey = n.getFullYear() + '-' + (n.getMonth() + 1) + '-' + n.getDate();
  } catch (e) {}

  // 动态加载样式（只在本文件生效时引入）
  function injectStyles() {
    if (document.getElementById('halloween-style')) return;
    var link = document.createElement('link');
    link.id = 'halloween-style';
    link.rel = 'stylesheet';
    link.href = 'halloween.css';
    document.head.appendChild(link);
  }

  // 漂浮装饰层：多个 emoji 用 CSS 变量随机分布/时长/大小，营造氛围
  function buildLayer() {
    var layer = document.createElement('div');
    layer.id = 'halloweenLayer';
    layer.setAttribute('aria-hidden', 'true');

    var decos = [
      { icon: '🎃', cls: 'pumpkin' },
      { icon: '🦇', cls: 'bat' },
      { icon: '👻', cls: 'ghost' },
      { icon: '🕸️', cls: 'web' },
      { icon: '🦇', cls: 'bat' },
      { icon: '🎃', cls: 'pumpkin' },
      { icon: '👻', cls: 'ghost' },
      { icon: '🎃', cls: 'pumpkin' }
    ];

    decos.forEach(function (d) {
      var s = document.createElement('span');
      s.className = 'h-deco h-deco--' + d.cls;
      s.textContent = d.icon;
      // 通过 CSS 变量注入随机参数，复用同一套 keyframe
      s.style.setProperty('--dx', ((Math.random() * 80) - 40).toFixed(1) + 'vw');
      s.style.setProperty('--dur', (13 + Math.random() * 12).toFixed(1) + 's');
      s.style.setProperty('--delay', (-Math.random() * 20).toFixed(1) + 's');
      s.style.setProperty('--size', (18 + Math.random() * 22).toFixed(0) + 'px');
      layer.appendChild(s);
    });

    document.body.appendChild(layer);
  }

  // 品牌标南瓜化 + 绑定交互
  function pumpBrand() {
    var mark = document.querySelector('.brand-mark');
    if (!mark) return;
    mark.classList.add('halloween-mark');
    mark.textContent = '🎃';
    mark.setAttribute('aria-label', '万圣节彩蛋：点我，不给糖就捣蛋');
    mark.setAttribute('role', 'button');
    mark.setAttribute('tabindex', '0');
    mark.addEventListener('click', treat);
    mark.addEventListener('keydown', function (e) {
      if (e.key === 'Enter' || e.key === ' ') {
        e.preventDefault();
        treat();
      }
    });
  }

  // 交互入口：糖果雨 + 轻提醒
  function treat(ev) {
    var origin = { x: window.innerWidth / 2, y: window.innerHeight / 2 };
    if (ev && typeof ev.clientX === 'number') {
      origin = { x: ev.clientX, y: ev.clientY };
    } else {
      var mark = document.querySelector('.brand-mark');
      if (mark) {
        var r = mark.getBoundingClientRect();
        origin = { x: r.left + r.width / 2, y: r.top + r.height / 2 };
      }
    }
    burst(origin);
    toast('🎃 万圣节快乐！不给糖就捣蛋～');
  }

  // 从指定点四散的糖果雨
  function burst(origin) {
    var candies = ['🍬', '🍭', '🍫', '🎃', '🦇', '✨'];
    var count = 22;
    for (var i = 0; i < count; i++) {
      var el = document.createElement('span');
      el.className = 'h-candy';
      el.textContent = candies[Math.floor(Math.random() * candies.length)];
      var angle = (Math.PI * 2 * i) / count + Math.random() * 0.5;
      var dist = 70 + Math.random() * 130;
      el.style.setProperty('--tx', (Math.cos(angle) * dist).toFixed(0) + 'px');
      el.style.setProperty('--ty', (Math.sin(angle) * dist - 60).toFixed(0) + 'px');
      el.style.setProperty('--size', (14 + Math.random() * 14).toFixed(0) + 'px');
      el.style.left = origin.x + 'px';
      el.style.top = origin.y + 'px';
      document.body.appendChild(el);
      (function (node) {
        setTimeout(function () { node.remove(); }, 1100);
      })(el);
    }
  }

  // 轻提醒：优先复用 app.js 的 showToast，否则用兜底气泡
  function toast(msg) {
    if (typeof window.showToast === 'function') {
      window.showToast(msg);
      return;
    }
    var t = document.createElement('div');
    t.className = 'h-toast';
    t.textContent = msg;
    document.body.appendChild(t);
    requestAnimationFrame(function () { t.classList.add('show'); });
    setTimeout(function () {
      t.classList.remove('show');
      setTimeout(function () { t.remove(); }, 300);
    }, 2600);
  }

  // 每天首次启动问候一次（等 app 初始化完成后再弹，避免被首帧覆盖）
  function greetOnce() {
    try {
      if (window.localStorage.getItem(GREET_KEY) === todayKey) return;
      window.localStorage.setItem(GREET_KEY, todayKey);
    } catch (e) {}
    setTimeout(function () {
      toast('🎃 万圣节快乐！Happy Halloween！');
    }, 1200);
  }

  function start() {
    injectStyles();
    buildLayer();
    pumpBrand();
    greetOnce();
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', start);
  } else {
    start();
  }
})();
