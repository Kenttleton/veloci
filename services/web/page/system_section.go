package page

import (
	"context"
	"fmt"
	"io"

	"github.com/a-h/templ"
)

// cfgSystemSection renders the System configuration tab content.
// Written as a Go component to avoid re-generating configuration_templ.go.
func cfgSystemSection(systemWindowDays int) templ.Component {
	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		_, err := fmt.Fprintf(w, `<div style="max-width:480px">
<h2 style="font-size:15px;font-weight:700;color:var(--text);margin:0 0 6px">System rate window</h2>
<p style="font-size:13px;color:var(--text3);margin:0 0 16px">Number of days used to compute the rolling-window rate for the Income and Spend system entries.</p>
<div style="display:flex;align-items:center;gap:10px">
<input id="system-window-input" type="number" min="1" max="365" value="%d" style="width:100px;background:var(--bg);border:1px solid var(--border);border-radius:5px;padding:6px 10px;font-size:13px;color:var(--text);font-family:inherit"/>
<button id="system-window-save" style="background:var(--accent);border:none;border-radius:5px;padding:6px 16px;cursor:pointer;font-size:13px;font-weight:500;color:#fff;font-family:inherit">Save</button>
<span id="system-window-msg" style="font-size:12px;color:var(--text3)"></span>
</div>
</div>
<script>
(function(){
var inp=document.getElementById('system-window-input');
var btn=document.getElementById('system-window-save');
var msg=document.getElementById('system-window-msg');
if(!btn)return;
btn.addEventListener('click',function(){
var v=parseInt(inp.value,10);
if(!v||v<1||v>365){msg.textContent='Enter a value between 1 and 365.';return;}
btn.disabled=true;msg.textContent='';
fetch('/api/entity/config',{method:'PUT',headers:{'Content-Type':'application/json'},credentials:'same-origin',body:JSON.stringify({system_window_days:v})})
.then(function(r){btn.disabled=false;if(r.ok){msg.textContent='Saved.';}else{msg.textContent='Failed to save.';}})
.catch(function(){btn.disabled=false;msg.textContent='Failed to save.';});
});
})();
</script>`, systemWindowDays)
		return err
	})
}
