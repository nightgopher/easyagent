import { api } from './api.js';

// 「模型连接」设置卡片：读取/更新 provider、接口地址、API 密钥与默认模型。
export class ProviderSettings {
  constructor() {
    this.provider = document.getElementById('provider-select');
    this.baseURL = document.getElementById('provider-base-url');
    this.apiKey = document.getElementById('provider-api-key');
    this.model = document.getElementById('provider-model');
    this.modelList = document.getElementById('provider-model-list');
    this.saveButton = document.getElementById('provider-save');
    this.refresh = document.getElementById('provider-refresh');
    this.status = document.getElementById('provider-status');
    this.pending = false;
    document.getElementById('provider-form').onsubmit = event => { event.preventDefault(); this.save(); };
    this.refresh.onclick = () => this.update();
  }
  fill(data) {
    const known = ['openai', 'anthropic'].includes(data?.provider);
    this.provider.value = known ? data.provider : 'openai';
    if (!known) this.provider.disabled = true; else this.provider.disabled = false;
    this.baseURL.value = data?.base_url || '';
    this.baseURL.placeholder = data?.base_url ? '留空保持不变（可选）' : '未设置（使用默认地址）';
    this.model.value = data?.model || '';
    this.apiKey.value = '';
    this.apiKey.placeholder = data?.api_key_set ? `已保存（${data.api_key_hint}）；留空保留现有密钥` : '输入 API 密钥';
  }
  async update() {
    if (this.pending) return;
    this.pending = true;
    this.saveButton.disabled = this.refresh.disabled = true;
    this.status.textContent = '正在读取…';
    this.status.classList.remove('is-error');
    try {
      this.fill(await api.get('/settings/provider'));
      this.status.textContent = '';
      void this.loadModelOptions();
    } catch (error) {
      this.status.textContent = `读取失败：${error.message}`;
      this.status.classList.add('is-error');
    } finally {
      this.pending = false;
      this.refresh.disabled = false;
      this.saveButton.disabled = false;
    }
  }
  // loadModelOptions 拉取 /models 目录填充下拉建议；失败时保持纯手输。
  async loadModelOptions() {
    try {
      const data = await api.get('/models');
      const models = Array.isArray(data?.models) ? data.models : [];
      this.modelList.innerHTML = models.map(m => {
        const id = m.id || m.name || '';
        const tag = m.supports_images ? '（支持图像）' : '';
        return `<option value="${id}">${id}${tag}</option>`;
      }).join('');
    } catch {
      this.modelList.innerHTML = '';
    }
  }
  async save() {
    if (this.pending) return;
    this.pending = true;
    this.saveButton.disabled = true;
    this.status.textContent = '正在保存…';
    this.status.classList.remove('is-error');
    try {
      this.fill(await api.put('/settings/provider', {
        provider: this.provider.value,
        base_url: this.baseURL.value.trim(),
        api_key: this.apiKey.value.trim(),
        model: this.model.value.trim(),
      }));
      this.status.textContent = '已保存。新会话将使用新连接；进行中的会话仍使用原连接。';
    } catch (error) {
      this.status.textContent = `保存失败：${error.message}`;
      this.status.classList.add('is-error');
    } finally {
      this.pending = false;
      this.saveButton.disabled = false;
      this.apiKey.value = '';
    }
  }
}
