import logoUrl from '../../assets/logo.svg';

interface ItervoxLogoProps {
  className?: string;
}

export function ItervoxLogo({ className }: ItervoxLogoProps) {
  return <img src={logoUrl} alt="Itervox" className={className} />;
}
