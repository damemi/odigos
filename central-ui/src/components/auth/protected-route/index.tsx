import { type FC, type ReactNode, useEffect, useMemo, useState } from 'react';
import { ROUTES } from '@/utils';
import { useUserStore } from '@/store';
import { VERIFY_TOKEN } from '@/graphql';
import { useMutation } from '@apollo/client/react';
import { usePathname, useRouter } from 'next/navigation';
import { type GeneralResult, SignInStatus } from '@/types';
import { CenterThis, Loader } from '@odigos/ui-kit/components';

interface ProtectedRouteProps {
  children: ReactNode;
}

const ProtectedRoute: FC<ProtectedRouteProps> = ({ children }) => {
  const router = useRouter();
  const pathname = usePathname();
  const { accessToken, status, clearAuth } = useUserStore();
  const [verified, setVerified] = useState(false);

  const isOnSignInPath = useMemo(() => pathname === ROUTES.SIGN_IN, [pathname]);
  const isOnUpdatePasswordPath = useMemo(() => pathname === ROUTES.UPDATE_YOUR_PASSWORD, [pathname]);

  const goToSignIn = () => {
    clearAuth();
    if (!isOnSignInPath) router.push(ROUTES.SIGN_IN);
  };
  const goToUpdatePassword = () => {
    if (!isOnUpdatePasswordPath) router.push(ROUTES.UPDATE_YOUR_PASSWORD);
  };
  const goToConnections = () => {
    router.push(ROUTES.CONNECTIONS);
  };

  const [verifyToken] = useMutation<{ verifyToken: GeneralResult }, { token: string }>(VERIFY_TOKEN);

  useEffect(() => {
    let cancelled = false;

    const run = async () => {
      if (!accessToken) {
        setVerified(false);
        goToSignIn();
        return;
      }

      try {
        const { data } = await verifyToken({ variables: { token: accessToken } });
        if (cancelled) return;

        const success = data?.verifyToken?.success;
        if (!success) {
          setVerified(false);
          goToSignIn();
          return;
        }

        setVerified(true);
        if (status === SignInStatus.PASSWORD_UPDATE_REQUIRED) {
          goToUpdatePassword();
        } else if (status === SignInStatus.ACCESS_DENIED) {
          setVerified(false);
          goToSignIn();
        } else if (isOnSignInPath || isOnUpdatePasswordPath) {
          goToConnections();
        }
      } catch (error) {
        if (cancelled) return;
        console.error(error);
        setVerified(false);
        goToSignIn();
      }
    };

    run();
    return () => {
      cancelled = true;
    };
  }, [accessToken]);

  if (!verified && !isOnSignInPath) {
    return (
      <CenterThis style={{ height: '100vh' }}>
        <Loader withSpinnerOld scaleSpinnerOld={2} />
      </CenterThis>
    );
  }

  return children;
};

export default ProtectedRoute;
