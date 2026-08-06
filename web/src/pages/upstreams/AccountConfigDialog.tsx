import { KeyRound, RefreshCw, ShieldCheck } from 'lucide-react';
import { useEffect, useMemo, useState, type FormEvent } from 'react';
import { Badge, Button, Dialog, Field, Switch } from '../../components/ui';
import { useToast } from '../../components/Toast';
import { api } from '../../lib/api';
import type {
  AccountAdapter,
  AccountAdapterKey,
  AccountAuthKind,
  ConfigureUpstreamAccountInput,
  Upstream,
  UpstreamAccount,
} from '../../lib/types';

interface AccountConfigDialogProps {
  open: boolean;
  upstream: Upstream;
  adapters: AccountAdapter[];
  account: UpstreamAccount;
  onClose: () => void;
  onSaved: (account: UpstreamAccount) => void;
}

const authLabels: Record<AccountAuthKind, { title: string; description: string }> = {
  api_token: { title: 'API Token', description: '使用站点提供的账户令牌读取原始账户数据。' },
  access_refresh: { title: '双 Token', description: '使用 Access Token，并由 Refresh Token 续期。' },
};

function defaultDashboardUrl(upstream: Upstream): string {
  return upstream.baseUrl.replace(/\/(?:v1|api)\/?$/i, '');
}

export function AccountConfigDialog({ open, upstream, adapters, account, onClose, onSaved }: AccountConfigDialogProps) {
  const toast = useToast();
  const [adapterKey, setAdapterKey] = useState<AccountAdapterKey>('new_api');
  const [authKind, setAuthKind] = useState<AccountAuthKind>('api_token');
  const [dashboardUrl, setDashboardUrl] = useState('');
  const [enabled, setEnabled] = useState(true);
  const [apiToken, setApiToken] = useState('');
  const [accessToken, setAccessToken] = useState('');
  const [refreshToken, setRefreshToken] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    if (!open) {
      setApiToken('');
      setAccessToken('');
      setRefreshToken('');
      setError(null);
      return;
    }

    const initialAdapterKey = account.adapter?.key ?? adapters[0]?.key ?? 'new_api';
    const initialAdapter = adapters.find((item) => item.key === initialAdapterKey);
    const initialAuthKind = account.auth?.kind && initialAdapter?.authKinds.includes(account.auth.kind)
      ? account.auth.kind
      : initialAdapter?.authKinds[0] ?? 'api_token';

    setAdapterKey(initialAdapterKey);
    setAuthKind(initialAuthKind);
    setDashboardUrl(account.dashboardUrl || defaultDashboardUrl(upstream));
    setEnabled(account.configured ? account.enabled : true);
    setApiToken('');
    setAccessToken('');
    setRefreshToken('');
    setError(null);
  }, [account, adapters, open, upstream]);

  const selectedAdapter = useMemo(
    () => adapters.find((item) => item.key === adapterKey) ?? null,
    [adapterKey, adapters],
  );

  const close = () => {
    setApiToken('');
    setAccessToken('');
    setRefreshToken('');
    setError(null);
    onClose();
  };

  const changeAdapter = (nextKey: AccountAdapterKey) => {
    const nextAdapter = adapters.find((item) => item.key === nextKey);
    const nextAuthKind = nextAdapter?.authKinds.includes(authKind) ? authKind : nextAdapter?.authKinds[0] ?? 'api_token';
    setAdapterKey(nextKey);
    setAuthKind(nextAuthKind);
    setApiToken('');
    setAccessToken('');
    setRefreshToken('');
    setError(null);
  };

  const changeAuthKind = (nextKind: AccountAuthKind) => {
    if (nextKind === authKind) return;
    setAuthKind(nextKind);
    setApiToken('');
    setAccessToken('');
    setRefreshToken('');
    setError(null);
  };

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    const normalizedUrl = dashboardUrl.trim().replace(/\/$/, '');
    if (!selectedAdapter) {
      setError('请选择可用的账户适配器。');
      return;
    }
    try {
      const parsed = new URL(normalizedUrl);
      if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') throw new Error();
    } catch {
      setError('请输入有效的站点面板地址。');
      return;
    }

    const keepsExistingAuth = account.configured && account.auth?.kind === authKind;
    if (authKind === 'api_token') {
      const hasExisting = keepsExistingAuth && account.auth?.hasApiToken;
      if (!apiToken.trim() && !hasExisting) {
        setError('请输入 API Token。切换认证方式后必须填写完整凭据。');
        return;
      }
    } else {
      const hasExistingAccess = keepsExistingAuth && account.auth?.hasAccessToken;
      const hasExistingRefresh = keepsExistingAuth && account.auth?.hasRefreshToken;
      if ((!accessToken.trim() && !hasExistingAccess) || (!refreshToken.trim() && !hasExistingRefresh)) {
        setError('请输入 Access Token 和 Refresh Token。切换认证方式后必须填写完整凭据。');
        return;
      }
    }

    const auth: ConfigureUpstreamAccountInput['auth'] = authKind === 'api_token'
      ? { kind: 'api_token', ...(apiToken.trim() ? { apiToken: apiToken.trim() } : {}) }
      : {
          kind: 'access_refresh',
          ...(accessToken.trim() ? { accessToken: accessToken.trim() } : {}),
          ...(refreshToken.trim() ? { refreshToken: refreshToken.trim() } : {}),
        };

    setSaving(true);
    setError(null);
    try {
      const updated = await api.configureUpstreamAccount(upstream.id, {
        adapterKey,
        dashboardUrl: normalizedUrl,
        enabled,
        auth,
        refreshNow: true,
      });
      onSaved(updated);
      if (updated.sync.state === 'error' || updated.sync.error) {
        toast.show('连接已保存，但同步失败', 'error');
      } else {
        toast.show('账户连接已保存并同步', 'success');
      }
      close();
    } catch (reason) {
      const message = reason instanceof Error ? reason.message : '账户连接保存失败';
      setError(message);
      toast.show(message, 'error');
    } finally {
      setSaving(false);
    }
  };

  const sameAuth = account.configured && account.auth?.kind === authKind;

  return (
    <Dialog
      open={open}
      title={account.configured ? '编辑账户连接' : '配置账户连接'}
      description="仅用于读取站点余额、套餐和原始使用记录。"
      onClose={close}
      width="lg"
      footer={<><Button onClick={close}>取消</Button><Button type="submit" form="account-config-form" variant="primary" busy={saving}>保存并同步</Button></>}
    >
      <form id="account-config-form" className="account-config-form" onSubmit={(event) => void submit(event)}>
        <div className="account-config-grid">
          <Field label="站点类型">
            <select className="select" value={adapterKey} disabled={adapters.length === 0} onChange={(event) => changeAdapter(event.target.value as AccountAdapterKey)}>
              {adapters.map((adapter) => <option key={adapter.key} value={adapter.key}>{adapter.label}</option>)}
            </select>
          </Field>
          <Field label="站点面板地址" hint="填写账户面板地址，不是模型 API 的 /v1 地址。">
            <input className="input" type="url" value={dashboardUrl} onChange={(event) => setDashboardUrl(event.target.value)} placeholder="https://example.com" />
          </Field>
        </div>

        {selectedAdapter && <div className="adapter-capabilities" aria-label="适配器能力">
          <span>{selectedAdapter.label} 可读取</span>
          {selectedAdapter.capabilities.balance && <Badge tone="success">余额</Badge>}
          {selectedAdapter.capabilities.subscription && <Badge tone="info">套餐</Badge>}
          {selectedAdapter.capabilities.usage && <Badge tone="neutral">使用记录</Badge>}
          {selectedAdapter.capabilities.tokenRefresh && <Badge tone="warning">自动续期</Badge>}
        </div>}

        <div className="account-form-section">
          <div className="account-form-heading">
            <span className="field-label">认证方式</span>
            <span>更换方式时需要重新填写完整凭据</span>
          </div>
          <div className="account-auth-selector" role="radiogroup" aria-label="账户认证方式">
            {selectedAdapter?.authKinds.map((kind) => {
              const Icon = kind === 'api_token' ? KeyRound : RefreshCw;
              return (
                <button type="button" role="radio" aria-checked={authKind === kind} className={authKind === kind ? 'is-active' : ''} key={kind} onClick={() => changeAuthKind(kind)}>
                  <Icon size={16} aria-hidden="true" />
                  <span><strong>{authLabels[kind].title}</strong><small>{authLabels[kind].description}</small></span>
                </button>
              );
            })}
          </div>
        </div>

        <div className="account-secret-fields">
          {authKind === 'api_token' ? (
            <Field label="API Token" hint={sameAuth && account.auth?.hasApiToken ? '留空会保留现有 Token。' : 'Token 只提交到服务端，不会写入浏览器存储。'}>
              <input className="input" type="password" autoComplete="new-password" value={apiToken} onChange={(event) => setApiToken(event.target.value)} placeholder={sameAuth && account.auth?.hasApiToken ? '留空保留现有 Token' : '输入 API Token'} />
            </Field>
          ) : (
            <>
              <Field label="Access Token" hint={sameAuth && account.auth?.hasAccessToken ? '留空会保留现有 Access Token。' : undefined}>
                <input className="input" type="password" autoComplete="new-password" value={accessToken} onChange={(event) => setAccessToken(event.target.value)} placeholder={sameAuth && account.auth?.hasAccessToken ? '留空保留现有 Token' : '输入 Access Token'} />
              </Field>
              <Field label="Refresh Token" hint={sameAuth && account.auth?.hasRefreshToken ? '留空会保留现有 Refresh Token。' : '用于服务端自动续期。'}>
                <input className="input" type="password" autoComplete="new-password" value={refreshToken} onChange={(event) => setRefreshToken(event.target.value)} placeholder={sameAuth && account.auth?.hasRefreshToken ? '留空保留现有 Token' : '输入 Refresh Token'} />
              </Field>
            </>
          )}
        </div>

        <div className="account-config-footer-row">
          <Switch checked={enabled} onChange={setEnabled} label="启用定时账户同步" />
          <span><ShieldCheck size={15} aria-hidden="true" />账户凭据与代理调用 Key 分开保存</span>
        </div>
        {error && <div className="account-form-error" role="alert">{error}</div>}
      </form>
    </Dialog>
  );
}
