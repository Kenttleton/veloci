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
    this.projection   = null;  // float: rate_per_day (cents)
    this._gran        = 'day'; // 'day' | 'month' | 'year'
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
    this._gran        = g;
    this.o.barWidth   = g === 'month' ? 16 : g === 'year' ? 24 : 5;
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

    // Auto-scale bar width to fill the chart when fewer bars than capacity
    var activeCellW = cellW, activeBw = bw;
    if (visible.length > 0 && visible.length < visCnt) {
      activeCellW = Math.floor(cW / visible.length);
      activeBw    = Math.max(1, activeCellW - o.barGap);
      subPx       = 0;
    }

    // Convert cents/day to dollars at the active granularity
    var granMult = this._gran === 'year' ? 365 / 100 : this._gran === 'month' ? 30.44 / 100 : 1 / 100;
    function toRate(v) { return v * granMult; }

    // Y range from visible data + projection; zero is always anchored so signed
    // rates (spend = negative, income = positive) render from a common baseline.
    var vals = [0];
    visible.forEach(function (d) {
      vals.push(toRate(d.actual_rate_per_day));
      if (d.high_rate != null) vals.push(toRate(d.high_rate));
      if (d.low_rate  != null) vals.push(toRate(d.low_rate));
    });
    if (this.projection != null) vals.push(toRate(this.projection));
    if (vals.length === 1) return; // only zero — no data

    // Symmetric range: zero is pinned at the vertical center.
    var absMax = Math.max.apply(null, vals.map(Math.abs));
    var pad    = Math.max(absMax * 0.12, 5);
    absMax    += pad;
    var yMin   = -absMax;
    var yMax   =  absMax;

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

    var proj   = this.projection;
    var zeroY  = toY(0);

    visible.forEach(function (d, i) {
      var x      = cR - subPx - (visible.length - i) * activeCellW;
      var actual = toRate(d.actual_rate_per_day);
      var col    = proj != null
        ? (actual >= toRate(proj) ? c.income : c.commit)
        : c.accent;

      if (d.open_rate != null) {
        // Candlestick (aggregated granularity)
        var high = toRate(d.high_rate);
        var low  = toRate(d.low_rate);
        var open = toRate(d.open_rate);
        var cls  = toRate(d.close_rate);
        var cx   = x + activeBw / 2;

        ctx.strokeStyle = col;
        ctx.lineWidth   = 1;
        ctx.beginPath();
        ctx.moveTo(cx, toY(high));
        ctx.lineTo(cx, toY(low));
        ctx.stroke();

        var bodyT = Math.min(toY(open), toY(cls));
        var bodyH = Math.max(Math.abs(toY(open) - toY(cls)), 1);
        ctx.fillStyle = col;
        ctx.fillRect(x, bodyT, activeBw, bodyH);
      } else {
        // Daily bar — grows from zero baseline (handles signed rates)
        var barT = Math.min(toY(actual), zeroY);
        var barH = Math.max(Math.abs(toY(actual) - zeroY), 1);
        ctx.fillStyle = col;
        ctx.fillRect(x, barT, activeBw, barH);
      }
    });

    ctx.restore();

    // Zero reference line — always at vertical center with symmetric range
    ctx.strokeStyle = c.grid;
    ctx.lineWidth   = 1;
    ctx.setLineDash([]);
    ctx.beginPath();
    ctx.moveTo(cL, zeroY); ctx.lineTo(cR, zeroY);
    ctx.stroke();

    // Projection dashed line
    if (proj != null) {
      var pY = toY(toRate(proj));
      ctx.strokeStyle = c.accent;
      ctx.lineWidth   = 1.5;
      ctx.setLineDash([5, 4]);
      ctx.beginPath();
      ctx.moveTo(cL, pY); ctx.lineTo(cR, pY);
      ctx.stroke();
      ctx.setLineDash([]);
    }

    // X axis labels (sparse)
    var gran = this._gran;
    var labelEvery = Math.max(1, Math.ceil(visible.length / 7));
    ctx.fillStyle    = c.axisText;
    ctx.font         = '10px system-ui,-apple-system,sans-serif';
    ctx.textAlign    = 'center';
    ctx.textBaseline = 'top';
    visible.forEach(function (d, i) {
      if (i % labelEvery !== 0) return;
      var lx = cR - subPx - (visible.length - i) * activeCellW + activeBw / 2;
      if (lx < cL || lx > cR) return;
      ctx.fillText(_fmtPeriod(d.period, gran), lx, cB + 5);
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
    var sign = v < 0 ? '-' : '';
    if (abs >= 10000) return sign + '$' + (abs / 1000).toFixed(0) + 'k';
    if (abs >= 1000)  return sign + '$' + (abs / 1000).toFixed(1) + 'k';
    return sign + '$' + abs.toFixed(0);
  }

  function _fmtPeriod(p, gran) {
    var months = ['Jan','Feb','Mar','Apr','May','Jun','Jul','Aug','Sep','Oct','Nov','Dec'];
    var parts  = p.split('-');
    var yr = parts[0], mo = parseInt(parts[1], 10) - 1, dd = parts[2];
    if (gran === 'year')  return yr;
    if (gran === 'month') return months[mo] + " '" + yr.slice(2);
    return months[mo] + ' ' + dd;
  }

  global.VelociChart = VelociChart;

})(window);
