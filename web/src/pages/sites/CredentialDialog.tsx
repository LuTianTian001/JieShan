import { Eye, EyeOff, Trash2 } from 'lucide-react';
import { useEffect, useMemo, useState, type FormEvent } from 'react';
import { Button, Dialog, Field, IconButton, InlineNotice, Switch, submitForm } from '../../components/ui';
import { endpointProfiles } from '../../lib/api';
import type { SiteCredential, SiteEndpoint } from '../../lib/types';

export interface CredentialEditorInput {
  name: string;
  secret: string;
  enabled: boolean;
  profileId: string;
  baseUrl: string;
}

export function CredentialDialog({
  open,
  saving,
  credential,
  endpoint,
  defaultBaseUrl,
  onClose,
  onSubmit,
  onDelete,
}: {
  open: boolean;
  saving: boolean;
  credential?: SiteCredential | null;
  endpoint?: SiteEndpoint | null;
  defaultBaseUrl: string;
  onClose: () => void;
  onSubmit: (input: CredentialEditorInput) => Promise<void>;
  onDelete?: () => void;
}) {
  const editing = Boolean(credential);
  const initialProfileId = useMemo(() => {
    if (!endpoint) return endpointProfiles[0].id;
    return endpointProfiles.find((profile) => profile.protocol === endpoint.wireProtocol && profile.surface === endpoint.surface)?.id
      || endpointProfiles.find((profile) => profile.protocol === endpoint.wireProtocol)?.id
      || endpointProfiles[0].id;
  }, [endpoint]);
  const [name, setName] = useState('');
  const [secret, setSecret] = useState('');
  const [enabled, setEnabled] = useState(true);
  const [profileId, setProfileId] = useState(initialProfileId);
  const [baseUrl, setBaseUrl] = useState(defaultBaseUrl);
  const [visible, setVisible] = useState(false);
  const [error, setError] = useState('');

  useEffect(() => {
    if (!open) return;
    setName(credential?.name || '主 API Key');
    setSecret('');
    setEnabled(credential?.enabled ?? true);
    setProfileId(initialProfileId);
    setBaseUrl(endpoint?.baseUrl || defaultBaseUrl);
    setVisible(false);
    setError('');
  }, [credential, defaultBaseUrl, endpoint, initialProfileId, open]);

  const selectedProfile = endpointProfiles.find((profile) => profile.id === profileId) || endpointProfiles[0];

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (!name.trim() || !baseUrl.trim()) {
      setError('名称和 API 地址不能为空');
      return;
    }
    if (!editing && !secret.trim()) {
      setError('请填写上游 API Key');
      return;
    }
    setError('');
    await onSubmit({
      name: name.trim(),
      secret: secret.trim(),
      enabled,
      profileId,
      baseUrl: baseUrl.trim(),
    });
  };

  return (
    <Dialog
      open={open}
      title={editing ? `编辑 ${credential?.name || 'API Key'}` : '添加上游 API Key'}
      description="每把 Key 独立选择 API 类型和地址，保存后可直接获取它支持的模型。"
      onClose={onClose}
      footer={<>
        {editing && onDelete && <Button icon={Trash2} onClick={onDelete}>删除</Button>}
        <span className="dialog-footer-spacer" />
        <Button onClick={onClose}>取消</Button>
        <Button variant="primary" busy={saving} onClick={() => submitForm('credential-form')}>{editing ? '保存修改' : '添加 API Key'}</Button>
      </>}
    >
      <form id="credential-form" className="form-stack" onSubmit={submit}>
        <div className="form-grid two-columns">
          <Field label="显示名称" required hint="例如 Ciii Primary、Claude 备用。">
            <input className="input" autoFocus value={name} onChange={(event) => setName(event.target.value)} maxLength={120} />
          </Field>
          <Field label="API 类型" required>
            <select className="select" value={profileId} onChange={(event) => setProfileId(event.target.value)}>
              {endpointProfiles.map((profile) => <option value={profile.id} key={profile.id}>{profile.label}</option>)}
            </select>
          </Field>
        </div>
        <Field label="API 地址" required hint={`当前类型：${selectedProfile.label}。填写请求根地址，不需要附加具体模型路径。`}>
          <input className="input code-input" type="url" value={baseUrl} onChange={(event) => setBaseUrl(event.target.value)} placeholder={selectedProfile.baseUrlPlaceholder} />
        </Field>
        <Field label={editing ? '更换 API Key（可选）' : '上游 API Key'} required={!editing} hint={editing ? '留空表示继续使用当前已加密密钥。' : undefined}>
          <div className="secret-input">
            <input className="input code-input" type={visible ? 'text' : 'password'} autoComplete="off" value={secret} onChange={(event) => setSecret(event.target.value)} placeholder={editing ? '留空则不更换' : 'sk-...'} />
            <IconButton label={visible ? '隐藏密钥' : '显示密钥'} onClick={() => setVisible((value) => !value)}>{visible ? <EyeOff size={16} /> : <Eye size={16} />}</IconButton>
          </div>
        </Field>
        <InlineNotice>API Key 只用于模型调用。站点账户登录只负责读取余额和上游日志，两套凭据互不混用。</InlineNotice>
        <Switch checked={enabled} label="允许这把 Key 参与模型路由" onChange={setEnabled} />
        {error && <p className="form-error">{error}</p>}
      </form>
    </Dialog>
  );
}
