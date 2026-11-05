import { useEffect, useState } from "react";
import { useNavigate } from "react-router-dom";
import {
  Box,
  Button,
  CircularProgress,
  TextField,
  Tooltip,
} from "@mui/material";
import { ArrowForward as ArrowForwardIcon } from "@mui/icons-material";
import AuthLayout, { AuthFrame } from "@/components/AuthLayout";
import { useSnackbar } from "@/contexts/SnackbarContext";
import PasswordField from "@/components/PasswordField";
import { initialize } from "@/queries/auth";
import { getStatus } from "@/queries/status";
import { passwordError } from "@/queries/users";

const EMAIL = /^[^\s@]+@[^\s@]+$/;

export default function Initialize() {
  const navigate = useNavigate();

  const [checking, setChecking] = useState(true);
  const [clustered, setClustered] = useState(false);
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [touched, setTouched] = useState({ email: false, password: false });
  const [submitting, setSubmitting] = useState(false);
  const { showSnackbar } = useSnackbar();

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const status = await getStatus();
        if (status.initialized) {
          navigate("/login", { replace: true });
          return;
        }
        if (!cancelled) setClustered(status.clustered);
      } catch {}
      if (!cancelled) setChecking(false);
    })();
    return () => {
      cancelled = true;
    };
  }, [navigate]);

  const emailError = EMAIL.test(email) ? undefined : "Enter an email address.";
  const newPasswordError = passwordError(password);

  const submit = async () => {
    setSubmitting(true);
    try {
      await initialize(email, password);
      navigate("/", { replace: true });
    } catch (err) {
      showSnackbar(
        err instanceof Error ? err.message : "Initialization failed.",
        "error",
      );
      setSubmitting(false);
    }
  };

  if (checking) {
    return (
      <AuthFrame>
        <Box>
          <CircularProgress aria-label="Loading" />
        </Box>
      </AuthFrame>
    );
  }

  return (
    <AuthLayout
      title="Initialize Ella SMSC"
      intro="Create the first user, an admin."
      submitLabel="Create"
      submitting={submitting}
      valid={!emailError && !newPasswordError}
      onSubmit={submit}
      footer={
        <Tooltip
          title={
            clustered
              ? ""
              : "This node has no cluster block in its configuration file."
          }
        >
          <Box component="span" sx={{ display: "block" }}>
            <Button
              fullWidth
              endIcon={<ArrowForwardIcon />}
              onClick={() => navigate("/initialize/join")}
              disabled={submitting || !clustered}
            >
              Join an existing cluster instead
            </Button>
          </Box>
        </Tooltip>
      }
    >
      <TextField
        label="Email"
        type="email"
        value={email}
        onChange={(e) => setEmail(e.target.value.trim())}
        onBlur={() => setTouched((t) => ({ ...t, email: true }))}
        error={touched.email && emailError !== undefined}
        helperText={touched.email ? emailError : undefined}
        fullWidth
        required
        autoFocus
        autoComplete="email"
      />
      <PasswordField
        label="Password"
        value={password}
        onChange={(e) => setPassword(e.target.value)}
        onBlur={() => setTouched((t) => ({ ...t, password: true }))}
        error={touched.password && newPasswordError !== undefined}
        helperText={touched.password ? newPasswordError : "8 to 72 characters."}
        fullWidth
        required
        autoComplete="new-password"
      />
    </AuthLayout>
  );
}
