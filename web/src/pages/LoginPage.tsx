import { ArrowRight, KeyRound, ShieldCheck } from 'lucide-react';
import { useState, type FormEvent } from 'react';
import { Button, Field } from '../components/ui';

export function LoginPage({ onLogin }: { onLogin: (password: string) => Promise<void> }) {
  const instanceHost = window.location.host || 'local';
  const [password, setPassword] = useState('');
  const [error, setError] = useState('');
  const [submitting, setSubmitting] = useState(false);

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (!password) {
      setError('请输入管理密码');
      return;
    }
    setSubmitting(true);
    setError('');
    try {
      await onLogin(password);
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : '登录失败，请检查密码');
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <main className="login-page">
      <section className="login-panel" aria-labelledby="login-title">
        <div className="login-brand">
          <img src="/jieshan-brand.jpg" alt="" />
          <div><strong>JieShan</strong><span>AI Gateway</span></div>
        </div>
        <div className="login-copy">
          <span className="login-symbol"><KeyRound size={19} /></span>
          <h1 id="login-title">进入管理面板</h1>
          <p>统一管理上游站点、模型路由、监控与下游额度。</p>
        </div>
        <div className="login-instance"><span>当前实例</span><code>{instanceHost}</code></div>
        <form onSubmit={submit} className="login-form">
          <Field label="管理密码" error={error || undefined}>
            <input
              className="input"
              type="password"
              autoComplete="current-password"
              value={password}
              onChange={(event) => setPassword(event.target.value)}
              autoFocus
            />
          </Field>
          <Button type="submit" variant="primary" icon={ArrowRight} busy={submitting}>登录</Button>
        </form>
        <div className="login-security"><ShieldCheck size={15} /><span>凭据只发送到当前 JieShan 实例</span></div>
      </section>
    </main>
  );
}
