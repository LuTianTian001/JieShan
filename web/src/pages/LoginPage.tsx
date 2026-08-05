import { ArrowRight, Eye, EyeOff, LoaderCircle, LockKeyhole } from 'lucide-react';
import { useState, type FormEvent } from 'react';
import { canUseDemoMode, enableDemoMode } from '../lib/api';
import type { User } from '../lib/types';

export function LoginPage({ onLogin, onDemo }: { onLogin: (password: string) => Promise<User>; onDemo: () => Promise<void> }) {
  const [password, setPassword] = useState('');
  const [showPassword, setShowPassword] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (!password) {
      setError('请输入管理员密码');
      return;
    }
    setLoading(true);
    setError(null);
    try {
      await onLogin(password);
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : '登录失败');
    } finally {
      setLoading(false);
    }
  };

  const enterDemo = async () => {
    setLoading(true);
    setError(null);
    try {
      enableDemoMode();
      await onDemo();
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : '无法进入预览');
    } finally {
      setLoading(false);
    }
  };

  return (
    <main className="login-page">
      <section className="login-panel">
        <div className="login-brand">
          <div className="brand-mark login-mark" aria-hidden="true">J</div>
          <div><strong>JieShan</strong><span>API Gateway</span></div>
        </div>
        <div className="login-heading">
          <h1>登录管理面板</h1>
          <p>使用部署时设置的管理员密码。</p>
        </div>
        <form className="login-form" onSubmit={submit}>
          <label className="field">
            <span className="field-label">管理员密码</span>
            <span className="password-input">
              <LockKeyhole size={16} aria-hidden="true" />
              <input className="input" type={showPassword ? 'text' : 'password'} value={password} onChange={(event) => setPassword(event.target.value)} autoComplete="current-password" autoFocus placeholder="输入密码" />
              <button type="button" aria-label={showPassword ? '隐藏密码' : '显示密码'} onClick={() => setShowPassword((value) => !value)}>
                {showPassword ? <EyeOff size={16} /> : <Eye size={16} />}
              </button>
            </span>
          </label>
          {error && <div className="login-error">{error}</div>}
          <button className="login-submit" type="submit" disabled={loading}>
            {loading ? <LoaderCircle className="spin" size={17} /> : <ArrowRight size={17} />}
            <span>{loading ? '正在验证' : '登录'}</span>
          </button>
        </form>
        {canUseDemoMode() && (
          <button className="demo-entry" type="button" onClick={() => void enterDemo()} disabled={loading}>
            进入本地预览
          </button>
        )}
        <footer>JieShan 管理端 · Cookie 会话认证</footer>
      </section>
    </main>
  );
}
