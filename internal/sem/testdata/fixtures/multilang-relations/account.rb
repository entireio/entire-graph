class Account
  def initialize(owner)
    @owner = owner
  end

  def rename(owner)
    apply(owner)
  end

  def apply(owner)
    @owner = owner
  end
end
