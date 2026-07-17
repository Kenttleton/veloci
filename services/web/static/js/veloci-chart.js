/**
 * VelociChart — composable canvas bar/candlestick chart.
 *
 * Usage:
 *   var chart = new VelociChart(canvasEl, {barWidth: 5})
 *   chart.load([{period:'2024-01-01', actual_rate_per_day:42.5, ...}])
 *   chart.setProjection(40.0)   // rate_per_day → dashed line
 *   chart.onNeedMore = function(oldest) { ... chart.prepend(older) }
 *   chart.setGranularity('month')  // 'day' | 'month'
 *   chart.destroy()
 */
(function (global) {
  'use strict';

  var C = {
    bg:       '#131c2b',
    grid:     '#1c2840',
    axisText: '#3e5070',
    income:   '#4db87a',
    commit:   '#cc5252',
    accent:   '#5f8dc7',
  };

  // ── Constructor ──────────────────────────────────────────────────────────────

  function VelociChart(canvas, opts) {
    this.canvas = canvas;
    this.ctx    = canvas.getContext('2d');
    this.o = Object.assign({
      barWidth:  5,
      barGap:    1,
      padTop:    10,
      padBottom: 28,
      padLeft:   52,
      padRight:  10,
      colors:    C,
    }, opts);

    this.data         = [];
    this.projection   = null;  // float: rate_per_day
    this._panPx       = 0;
    this._drag        = null;
    this._loadingMore = false;
    this.onNeedMore   = null;  // fn(oldestPeriod: string)

    this._initEvents();
    this._measure();
  }

  // ── Public API ───────────────────────────────────────────────────────────────

  VelociChart.prototype.load = function (points) {
    this.data         = _sort(points);
    this._panPx       = 0;
    this._loadingMore = false;
    this._render();
  };

  VelociChart.prototype.prepend = function (older) {
    var seen = Object.create(null);
    this.data = _sort(older.concat(this.data)).filter(function (d) {
      return seen[d.period] ? false : (seen[d.period] = true);
    });
    this._loadingMore = false;
    this._render();
  };

  VelociChart.prototype.setProjection = function (ratePerDay) {
    this.projection = (ratePerDay != null && !isNaN(ratePerDay)) ? +ratePerDay : null;
    this._render();
  };

  VelociChart.prototype.setGranularity = function (g) {
    this.o.barWidth   = g === 'month' ? 16 : 5;
    this.data         = [];
    this._panPx       = 0;
    this._loadingMore = false;
    this._render();
  };

  VelociChart.prototype.destroy = function () { /* ResizeObserver GC'd with element */ };

  // ── Render ───────────────────────────────────────────────────────────────────

  VelociChart.prototype._render = function () {
    var ctx = this.ctx, o = this.o, c = o.colors;
    var W = this.w, H = this.h, data = this.data;

    ctx.clearRect(0, 0, W, H);
    ctx.fillStyle = c.bg;
    ctx.fillRect(0, 0, W, H);

    if (!data.length) {
      ctx.fillStyle   = c.axisText;
      ctx.font        = '13px system-ui,-apple-system,sans-serif';
      ctx.textAlign   = 'center';
      ctx.textBaseline= 'middle';
      ctx.fillText('Select an entry to view history', W / 2, H / 2);
      return;
    }

    var cL = o.padLeft, cR = W - o.padRight;
    var cT = o.padTop,  cB = H - o.padBottom;
    var cW = cR - cL,   cH = cB - cT;
    var bw = o.barWidth, cellW = bw + o.barGap;
    var visCnt = Math.max(1, Math.floor(cW / cellW));

    // Viewport: right edge shows latest data, pan left shows older
    var rightOffset = Math.floor(this._panPx / cellW);
    var endIdx      = data.length - rightOffset;
    var startIdx    = Math.max(0, endIdx - visCnt);
    endIdx          = Math.min(data.length, Math.max(startIdx + 1, endIdx));
    var visible     = data.slice(startIdx, endIdx);
    var subPx       = this._panPx % cellW;

    // Y range from visible data + projection
    var vals = [];
    visible.forEach(function (d) {
      vals.push(d.actual_rate_per_day * 30.44);
      if (d.high_rate != null) vals.push(d.high_rate * 30.44);
      if (d.low_rate  != null) vals.push(d.low_rate  * 30.44);
    });
    if (this.projection != null) vals.push(this.projection * 30.44);
    if (!vals.length) return;

    var yMin = Math.min.apply(null, vals);
    var yMax = Math.max.apply(null, vals);
    var pad  = Math.max((yMax - yMin) * 0.12, 5);
    yMin -= pad; yMax += pad;

    function toY(v) { return cB - ((v - yMin) / (yMax - yMin)) * cH; }

    // Grid + Y labels
    ctx.strokeStyle  = c.grid;
    ctx.lineWidth    = 0.5;
    ctx.fillStyle    = c.axisText;
    ctx.font         = '10px system-ui,-apple-system,sans-serif';
    ctx.textAlign    = 'right';
    ctx.textBaseline = 'middle';
    for (var i = 0; i <= 4; i++) {
      var v  = yMin + (yMax - yMin) * (i / 4);
      var gy = toY(v);
      ctx.beginPath(); ctx.moveTo(cL, gy); ctx.lineTo(cR, gy); ctx.stroke();
      ctx.fillText(_fmtMoney(v), cL - 4, gy);
    }

    // Clip bars to chart area
    ctx.save();
    ctx.beginPath();
    ctx.rect(cL, cT, cW, cH + 1);
    ctx.clip();

    var proj = this.projection;

    visible.forEach(function (d, i) {
      var x      = cR - subPx - (visible.length - i) * cellW;
      var actual = d.actual_rate_per_day * 30.44;
      var col    = proj != null
        ? (actual >= proj * 30.44 ? c.income : c.commit)
        : c.accent;

      if (d.open_rate != null) {
        // Candlestick (aggregated granularity)
        var high = d.high_rate * 30.44;
        var low  = d.low_rate  * 30.44;
        var open = d.open_rate * 30.44;
        var cls  = d.close_rate * 30.44;
        var cx   = x + bw / 2;

        ctx.strokeStyle = col;
        ctx.lineWidth   = 1;
        ctx.beginPath();
        ctx.moveTo(cx, toY(high));
        ctx.lineTo(cx, toY(low));
        ctx.stroke();

        var bodyT = Math.min(toY(open), toY(cls));
        var bodyH = Math.max(Math.abs(toY(open) - toY(cls)), 1);
        ctx.fillStyle = col;
        ctx.fillRect(x, bodyT, bw, bodyH);
      } else {
        // Daily bar — grows from chart bottom
        var barT = toY(actual);
        var barH = Math.max(cB - barT, 1);
        ctx.fillStyle = col;
        ctx.fillRect(x, barT, bw, barH);
      }
    });

    ctx.restore();

    // Projection dashed line
    if (proj != null) {
      var pY = toY(proj * 30.44);
      ctx.strokeStyle = c.accent;
      ctx.lineWidth   = 1.5;
      ctx.setLineDash([5, 4]);
      ctx.beginPath();
      ctx.moveTo(cL, pY); ctx.lineTo(cR, pY);
      ctx.stroke();
      ctx.setLineDash([]);
    }

    // X axis labels (sparse)
    var labelEvery = Math.max(1, Math.ceil(visible.length / 7));
    ctx.fillStyle    = c.axisText;
    ctx.font         = '10px system-ui,-apple-system,sans-serif';
    ctx.textAlign    = 'center';
    ctx.textBaseline = 'top';
    visible.forEach(function (d, i) {
      if (i % labelEvery !== 0) return;
      var lx = cR - subPx - (visible.length - i) * cellW + bw / 2;
      if (lx < cL || lx > cR) return;
      ctx.fillText(_fmtPeriod(d.period), lx, cB + 5);
    });

    // Request older data when panned to start
    if (startIdx === 0 && !this._loadingMore && this.onNeedMore) {
      this._loadingMore = true;
      this.onNeedMore(data[0].period);
    }
  };

  // ── Sizing ───────────────────────────────────────────────────────────────────

  VelociChart.prototype._measure = function () {
    var el  = this.canvas.parentElement;
    var dpr = window.devicePixelRatio || 1;
    var w   = el.offsetWidth  || 400;
    var h   = el.offsetHeight || 220;
    this.canvas.width  = w * dpr;
    this.canvas.height = h * dpr;
    this.canvas.style.width  = w + 'px';
    this.canvas.style.height = h + 'px';
    this.ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    this.w = w;
    this.h = h;
  };

  // ── Events ───────────────────────────────────────────────────────────────────

  VelociChart.prototype._initEvents = function () {
    var self = this, canvas = this.canvas;

    function onDown(cx) { self._drag = { sx: cx, sp: self._panPx }; }
    function onMove(cx) {
      if (!self._drag) return;
      var dx  = self._drag.sx - cx;
      var max = Math.max(0, (self.data.length - 4) * (self.o.barWidth + self.o.barGap));
      self._panPx = Math.min(max, Math.max(0, self._drag.sp + dx));
      self._render();
    }
    function onUp() { self._drag = null; canvas.style.cursor = 'grab'; }

    canvas.addEventListener('mousedown', function (e) {
      onDown(e.clientX);
      canvas.style.cursor = 'grabbing';
    });
    document.addEventListener('mousemove', function (e) { if (self._drag) onMove(e.clientX); });
    document.addEventListener('mouseup',   function ()  { if (self._drag) onUp(); });
    canvas.addEventListener('touchstart', function (e) { onDown(e.touches[0].clientX); }, { passive: true });
    canvas.addEventListener('touchmove',  function (e) { e.preventDefault(); onMove(e.touches[0].clientX); }, { passive: false });
    canvas.addEventListener('touchend',   onUp);

    new ResizeObserver(function () { self._measure(); self._render(); }).observe(canvas.parentElement);

    canvas.style.cursor = 'grab';
  };

  // ── Helpers ──────────────────────────────────────────────────────────────────

  function _sort(arr) {
    return arr.slice().sort(function (a, b) {
      return a.period < b.period ? -1 : a.period > b.period ? 1 : 0;
    });
  }

  function _fmtMoney(v) {
    var abs = Math.abs(v);
    if (abs >= 10000) return '$' + (v / 1000).toFixed(0) + 'k';
    if (abs >= 1000)  return '$' + (v / 1000).toFixed(1) + 'k';
    return '$' + v.toFixed(0);
  }

  function _fmtPeriod(p) {
    var months = ['Jan','Feb','Mar','Apr','May','Jun','Jul','Aug','Sep','Oct','Nov','Dec'];
    var parts  = p.split('-');
    return months[parseInt(parts[1], 10) - 1] + ' ' + parts[2];
  }

  global.VelociChart = VelociChart;

})(window);
