"use strict";

const hardwareRoute = window.location.pathname;
const byID = (id) => document.getElementById(id);
const stateLabels = {
  unbound: "未绑定", missing: "未连接", conflict: "身份冲突", waiting: "等待设备就绪", blocked: "接口当前不可用",
  connected: "宿主机设备已就绪", error: "检查或应用失败", ready: "就绪",
  partial: "部分就绪", enumerated: "已枚举", timeout: "检查超时",
  unknown: "未确认", "optional-absent": "选装设备未检测到",
};

async function hardwareFetch(path, options) {
  const response = await fetch(path, options);
  if (response.status === 401) {
    window.location.replace("/login");
    throw new Error("请重新登录。");
  }
  const data = await response.json();
  if (!response.ok) throw new Error(data.error || "设备信息暂时不可用。");
  return data;
}

if (hardwareRoute === "/") {
  const select = byID("coordinator");
  const status = byID("mapping-status");
  const refresh = byID("mapping-refresh");
  const bind = byID("mapping-bind");
  const unbind = byID("mapping-unbind");
  let candidates = [];
  let mapping;
  let busy = false;

  function identityText(id) {
    if (!id) return "";
    if (id.kind === "uart") return id.topology;
    return [id.vendor + ":" + id.product, id.interface, id.serial || id.topology].filter(Boolean).join(" · ");
  }

  function syncControls() {
    const candidate = candidates.find((c) => c.id === select.value);
    byID("candidate-details").textContent = candidate ? identityText(candidate.identity) : "";
    bind.textContent = mapping?.binding ? "更换绑定" : "绑定设备";
    bind.disabled = busy || !candidate?.available;
    unbind.disabled = busy || !mapping?.binding;
    refresh.disabled = busy;
  }

  function display(result) {
    const previous = select.value;
    mapping = result;
    candidates = result.candidates || [];
    const device = result.device ? result.device + " → " + result.alias : identityText(result.binding);
    status.textContent = [stateLabels[result.state] || result.state, device].filter(Boolean).join(" · ");
    select.replaceChildren(new Option("请选择设备", ""));
    for (const c of candidates) {
      const option = new Option(c.label + " · " + c.path, c.id);
      option.disabled = !c.available;
      select.add(option);
    }
    if (candidates.some((c) => c.id === previous && c.available)) select.value = previous;
    byID("mapping-controls").hidden = false;
  }

  async function load(operation, candidate) {
    if (busy) return;
    busy = true;
    syncControls();
    try {
      const data = operation ? await hardwareFetch("/api/v1/hardware/binding", {
        method: "POST",
        headers: {"Content-Type": "application/json"},
        body: JSON.stringify({operation, candidate}),
      }) : await hardwareFetch("/api/v1/hardware");
      display(data.mapping);
    } catch (error) {
      status.textContent = error.message;
    } finally {
      busy = false;
      syncControls();
    }
  }

  select.addEventListener("change", syncControls);
  refresh.addEventListener("click", () => load());
  bind.addEventListener("click", () => load("bind", select.value));
  unbind.addEventListener("click", () => load("unbind"));
  byID("copy-compose").addEventListener("click", async (event) => {
    const button = event.currentTarget;
    try {
      await navigator.clipboard.writeText(byID("compose-devices").textContent);
      button.textContent = "已复制";
    } catch {
      byID("message").textContent = "无法访问剪贴板，可以手动选中并复制。";
    }
  });
  load();
  setInterval(() => {
    if (!document.hidden) load();
  }, 5000);
}

if (hardwareRoute === "/hardware/boot-checks") {
  const select = byID("boot-report");
  const status = byID("report-status");
  let reports = [];

  function display() {
    const report = reports.find((item) => item.bootId === select.value);
    const list = byID("boot-check-list");
    list.replaceChildren();
    if (!report) return;
    for (const check of report.checks) {
      const name = document.createElement("dt");
      name.textContent = check.name + " · " + (stateLabels[check.state] || check.state);
      const detail = document.createElement("dd");
      detail.textContent = check.detail;
      list.append(name, detail);
    }
  }

  async function loadReports() {
    try {
      const reply = await hardwareFetch("/api/v1/hardware/boot-reports");
      const previous = select.selectedIndex > 0 ? select.value : "";
      reports = reply.reports || [];
      select.replaceChildren();
      for (const [index, report] of reports.entries()) {
        select.add(new Option((index === 0 ? "最近一次 · " : "") +
          new Date(report.checked).toLocaleString(), report.bootId));
      }
      if (previous && reports.some((report) => report.bootId === previous)) select.value = previous;
      status.textContent = reply.error || (reply.checking ? "本次检查正在进行…" :
        reports.length ? "保留最近三次开机检查记录。" : "暂无完整的启动检查记录。");
      byID("report-history").hidden = reports.length === 0;
      display();
      if (reply.checking) setTimeout(loadReports, 2000);
    } catch (error) {
      status.textContent = error.message;
    }
  }

  select.addEventListener("change", display);
  loadReports();
}
