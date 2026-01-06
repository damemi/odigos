import { type FC, type ReactNode, useEffect, useMemo } from 'react';
import { ROUTES } from '@/utils';
import { useUserStore } from '@/store';
import { VERIFY_TOKEN } from '@/graphql';
import { useMutation } from '@apollo/client';
import { usePathname, useRouter } from 'next/navigation';
import { type GeneralResult, SignInStatus } from '@/types';
import { CenterThis, FadeLoader } from '@odigos/ui-kit/components';

interface ProtectedRouteProps {
  children: ReactNode;
}

const ProtectedRoute: FC<ProtectedRouteProps> = ({ children }) => {
  const router = useRouter();
  const pathname = usePathname();
  const { accessToken, status, clearAuth } = useUserStore();

  const isOnSignInPath = useMemo(() => pathname === ROUTES.SIGN_IN, [pathname]);
  const isNeedPasswordUpdate = useMemo(() => status === SignInStatus.PASSWORD_UPDATE_REQUIRED, [status]);
  const isOnUpdatePasswordPath = useMemo(() => pathname === ROUTES.UPDATE_YOUR_PASSWORD, [pathname]);

  const goToSignIn = () => {
    clearAuth();
    router.push(ROUTES.SIGN_IN);
  };
  const goToUpdatePassword = () => {
    router.push(ROUTES.UPDATE_YOUR_PASSWORD);
  };
  const goToConnections = () => {
    router.push(ROUTES.CONNECTIONS);
  };

  const [verifyToken, { loading }] = useMutation<{ verifyToken: GeneralResult }, { token: string }>(VERIFY_TOKEN, {
    onError: (error) => {
      console.error(error);
      goToSignIn();
    },
    onCompleted: (data) => {
      if (!data.verifyToken.success) {
        goToSignIn();
      }

      if (data.verifyToken.success) {
        if (isNeedPasswordUpdate && !isOnUpdatePasswordPath) {
          goToUpdatePassword();
        }
        if (isOnSignInPath) {
          goToConnections();
        }
      }
    },
  });

  useEffect(() => {
    if (!accessToken && !isOnSignInPath) goToSignIn();
    if (accessToken) verifyToken({ variables: { token: accessToken } });
  }, [accessToken, isOnSignInPath]);

  if (!isOnSignInPath && !isOnUpdatePasswordPath && (loading || !accessToken)) {
    return (
      <CenterThis style={{ height: '100vh' }}>
        <FadeLoader scale={2} />
      </CenterThis>
    );
  }

  return children;
};

export default ProtectedRoute;
